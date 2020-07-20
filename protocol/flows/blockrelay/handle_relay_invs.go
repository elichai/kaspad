package blockrelay

import (
	"github.com/kaspanet/kaspad/blockdag"
	"github.com/kaspanet/kaspad/netadapter"
	"github.com/kaspanet/kaspad/netadapter/router"
	"github.com/kaspanet/kaspad/protocol/blocklogger"
	"github.com/kaspanet/kaspad/protocol/common"
	"github.com/kaspanet/kaspad/protocol/flows/ibd"
	peerpkg "github.com/kaspanet/kaspad/protocol/peer"
	"github.com/kaspanet/kaspad/protocol/protocolerrors"
	"github.com/kaspanet/kaspad/util"
	"github.com/kaspanet/kaspad/util/daghash"
	mathUtil "github.com/kaspanet/kaspad/util/math"
	"github.com/kaspanet/kaspad/wire"
	"github.com/pkg/errors"
)

// NewBlockHandler is a function that is to be
// called when a new block is successfully processed.
type NewBlockHandler func(block *util.Block) error

// HandleRelayInvs listens to wire.MsgInvRelayBlock messages, requests their corresponding blocks if they
// are missing, adds them to the DAG and propagates them to the rest of the network.
func HandleRelayInvs(incomingRoute *router.Route, outgoingRoute *router.Route,
	peer *peerpkg.Peer, netAdapter *netadapter.NetAdapter, dag *blockdag.BlockDAG, newBlockHandler NewBlockHandler) error {

	invsQueue := make([]*wire.MsgInvRelayBlock, 0)
	for {
		inv, shouldStop, err := readInv(incomingRoute, &invsQueue)
		if err != nil {
			return err
		}
		if shouldStop {
			return nil
		}

		if dag.IsKnownBlock(inv.Hash) {
			if dag.IsKnownInvalid(inv.Hash) {
				return protocolerrors.Errorf(true, "sent inv of an invalid block %s",
					inv.Hash)
			}
			continue
		}

		ibd.StartIBDIfRequired(dag)
		if ibd.IsInIBD() {
			// Block relay is disabled during IBD
			continue
		}

		requestQueue := newHashesQueueSet()
		requestQueue.enqueueIfNotExists(inv.Hash)

		for requestQueue.len() > 0 {
			err := requestBlocks(netAdapter, outgoingRoute, peer, incomingRoute, dag, &invsQueue,
				requestQueue, newBlockHandler)
			if err != nil {
				return err
			}
		}
	}
}

func readInv(incomingRoute *router.Route, invsQueue *[]*wire.MsgInvRelayBlock) (
	inv *wire.MsgInvRelayBlock, shouldStop bool, err error) {

	if len(*invsQueue) > 0 {
		inv, *invsQueue = (*invsQueue)[0], (*invsQueue)[1:]
		return inv, false, nil
	}

	msg, isOpen := incomingRoute.Dequeue()
	if !isOpen {
		return nil, true, nil
	}

	inv, ok := msg.(*wire.MsgInvRelayBlock)
	if !ok {
		return nil, false, protocolerrors.Errorf(true, "unexpected %s message in the block relay flow while "+
			"expecting an inv message", msg.Command())
	}
	return inv, false, nil
}

func requestBlocks(netAdapater *netadapter.NetAdapter, outgoingRoute *router.Route,
	peer *peerpkg.Peer, incomingRoute *router.Route, dag *blockdag.BlockDAG,
	invsQueue *[]*wire.MsgInvRelayBlock, requestQueue *hashesQueueSet,
	newBlockHandler NewBlockHandler) error {

	numHashesToRequest := mathUtil.MinInt(wire.MsgGetRelayBlocksHashes, requestQueue.len())
	hashesToRequest := requestQueue.dequeue(numHashesToRequest)

	pendingBlocks := map[daghash.Hash]struct{}{}
	var filteredHashesToRequest []*daghash.Hash
	for _, hash := range hashesToRequest {
		exists := requestedBlocks.addIfNotExists(hash)
		if !exists {
			continue
		}

		pendingBlocks[*hash] = struct{}{}
		filteredHashesToRequest = append(filteredHashesToRequest, hash)
	}

	// In case the function returns earlier than expected, we want to make sure requestedBlocks is
	// clean from any pending blocks.
	defer requestedBlocks.removeSet(pendingBlocks)

	getRelayBlocksMsg := wire.NewMsgGetRelayBlocks(filteredHashesToRequest)
	isOpen := outgoingRoute.Enqueue(getRelayBlocksMsg)
	if !isOpen {
		return errors.WithStack(common.ErrRouteClosed)
	}

	for len(pendingBlocks) > 0 {
		msgBlock, err := readMsgBlock(incomingRoute, invsQueue)
		if err != nil {
			return err
		}

		block := util.NewBlock(msgBlock)
		blockHash := block.Hash()
		if _, ok := pendingBlocks[*blockHash]; !ok {
			return protocolerrors.Errorf(true, "got unrequested block %s", block.Hash())
		}
		delete(pendingBlocks, *blockHash)
		requestedBlocks.remove(blockHash)

		err = processAndRelayBlock(netAdapater, peer, dag, requestQueue, block, newBlockHandler)
		if err != nil {
			return err
		}
	}
	return nil
}

// readMsgBlock returns the next msgBlock in msgChan, and populates invsQueue with any inv messages that meanwhile arrive.
//
// Note: this function assumes msgChan can contain only wire.MsgInvRelayBlock and wire.MsgBlock messages.
func readMsgBlock(incomingRoute *router.Route, invsQueue *[]*wire.MsgInvRelayBlock) (
	msgBlock *wire.MsgBlock, err error) {

	for {
		message, isOpen, err := incomingRoute.DequeueWithTimeout(common.DefaultTimeout)
		if err != nil {
			return nil, err
		}
		if !isOpen {
			return nil, errors.WithStack(common.ErrRouteClosed)
		}

		switch message := message.(type) {
		case *wire.MsgInvRelayBlock:
			*invsQueue = append(*invsQueue, message)
		case *wire.MsgBlock:
			return message, nil
		default:
			panic(errors.Errorf("unexpected message %s", message.Command()))
		}
	}
}

func processAndRelayBlock(netAdapter *netadapter.NetAdapter, peer *peerpkg.Peer,
	dag *blockdag.BlockDAG, requestQueue *hashesQueueSet, block *util.Block,
	newBlockHandler NewBlockHandler) error {

	blockHash := block.Hash()
	isOrphan, isDelayed, err := dag.ProcessBlock(block, blockdag.BFNone)
	if err != nil {
		// When the error is a rule error, it means the block was simply
		// rejected as opposed to something actually going wrong, so log
		// it as such. Otherwise, something really did go wrong, so panic.
		if !errors.As(err, &blockdag.RuleError{}) {
			panic(errors.Wrapf(err, "failed to process block %s",
				blockHash))
		}
		log.Infof("Rejected block %s from %s: %s", blockHash,
			peer, err)

		return protocolerrors.Wrap(true, err, "got invalid block")
	}

	if isDelayed {
		return nil
	}

	if isOrphan {
		blueScore, err := block.BlueScore()
		if err != nil {
			return protocolerrors.Errorf(true, "received an orphan "+
				"block %s with malformed blue score", blockHash)
		}

		const maxOrphanBlueScoreDiff = 10000
		selectedTipBlueScore := dag.SelectedTipBlueScore()
		if blueScore > selectedTipBlueScore+maxOrphanBlueScoreDiff {
			log.Infof("Orphan block %s has blue score %d and the selected tip blue score is "+
				"%d. Ignoring orphans with a blue score difference from the selected tip greater than %d",
				blockHash, blueScore, selectedTipBlueScore, maxOrphanBlueScoreDiff)
			return nil
		}

		// Request the parents for the orphan block from the peer that sent it.
		missingAncestors := dag.GetOrphanMissingAncestorHashes(blockHash)
		for _, missingAncestor := range missingAncestors {
			requestQueue.enqueueIfNotExists(missingAncestor)
		}
		return nil
	}
	err = blocklogger.LogBlock(block)
	if err != nil {
		return err
	}
	//TODO(libp2p)
	//// When the block is not an orphan, log information about it and
	//// update the DAG state.
	// sm.restartSyncIfNeeded()
	//// Clear the rejected transactions.
	//sm.rejectedTxns = make(map[daghash.TxID]struct{})
	err = netAdapter.Broadcast(peerpkg.ReadyPeerIDs(), wire.NewMsgInvBlock(blockHash))
	if err != nil {
		return err
	}

	ibd.StartIBDIfRequired(dag)
	err = newBlockHandler(block)
	if err != nil {
		panic(err)
	}

	return nil
}
