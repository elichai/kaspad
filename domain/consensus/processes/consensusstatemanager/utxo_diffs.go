package consensusstatemanager

import (
	"github.com/kaspanet/kaspad/domain/consensus/model/externalapi"
)

func (csm *consensusStateManager) stageDiff(blockHash *externalapi.DomainHash,
	utxoDiff externalapi.UTXODiff, utxoDiffChild *externalapi.DomainHash) {

	log.Debugf("stageDiff start for block %s", blockHash)
	defer log.Debugf("stageDiff end for block %s", blockHash)

	log.Debugf("Staging block %s as the diff child of %s", utxoDiffChild, blockHash)
	csm.utxoDiffStore.Stage(blockHash, utxoDiff, utxoDiffChild)
}
