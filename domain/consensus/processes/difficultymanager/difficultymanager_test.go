package difficultymanager_test

import (
	"github.com/kaspanet/kaspad/domain/consensus"
	"github.com/kaspanet/kaspad/domain/consensus/model"
	"github.com/kaspanet/kaspad/domain/consensus/model/externalapi"
	"github.com/kaspanet/kaspad/domain/consensus/utils/consensusserialization"
	"github.com/kaspanet/kaspad/domain/consensus/utils/testutils"
	"github.com/kaspanet/kaspad/domain/dagconfig"
	"github.com/kaspanet/kaspad/util"
	"testing"
)

func TestDifficulty(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, params *dagconfig.Params) {
		params.K = 1
		params.DifficultyAdjustmentWindowSize = 264

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(params, "TestDifficulty")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown()

		addBlock := func(blockTime int64, parents ...*externalapi.DomainHash) (*externalapi.DomainBlock, *externalapi.DomainHash) {
			bluestParent, err := tc.GHOSTDAGManager().ChooseSelectedParent(parents...)
			if err != nil {
				t.Fatalf("ChooseSelectedParent: %+v", err)
			}

			if blockTime == 0 {
				header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), bluestParent)
				if err != nil {
					t.Fatalf("BlockHeader: %+v", err)
				}

				blockTime = header.TimeInMilliseconds + params.TargetTimePerBlock.Milliseconds()
			}

			block, err := tc.BuildBlockWithParents(parents, nil, nil)
			if err != nil {
				t.Fatalf("BuildBlockWithParents: %+v", err)
			}

			block.Header.TimeInMilliseconds = blockTime
			err = tc.ValidateAndInsertBlock(block)
			if err != nil {
				t.Fatalf("ValidateAndInsertBlock: %+v", err)
			}

			return block, consensusserialization.BlockHash(block)
		}

		minimumTime := func(parents ...*externalapi.DomainHash) int64 {
			var tempHash externalapi.DomainHash
			err := tc.BlockRelationStore().StageBlockRelation(&tempHash, &model.BlockRelations{
				Parents:  parents,
				Children: nil,
			})
			if err != nil {
				t.Fatalf("StageBlockRelation: %+v", err)
			}
			defer tc.BlockRelationStore().Discard()

			err = tc.GHOSTDAGManager().GHOSTDAG(&tempHash)
			if err != nil {
				t.Fatalf("GHOSTDAG: %+v", err)
			}
			defer tc.GHOSTDAGDataStore().Discard()

			pastMedianTime, err := tc.PastMedianTimeManager().PastMedianTime(&tempHash)
			if err != nil {
				t.Fatalf("PastMedianTime: %+v", err)
			}

			return pastMedianTime + 1
		}

		addBlockWithMinimumTime := func(parents ...*externalapi.DomainHash) (*externalapi.DomainBlock, *externalapi.DomainHash) {
			minTime := minimumTime(parents...)
			return addBlock(minTime, parents...)
		}

		tipHash := params.GenesisHash
		tip := params.GenesisBlock
		for i := uint64(0); i < params.DifficultyAdjustmentWindowSize; i++ {
			tip, tipHash = addBlock(0, tipHash)
			if tip.Header.Bits != params.GenesisBlock.Header.Bits {
				t.Fatalf("As long as the bluest parent's blue score is less then the difficulty adjustment " +
					"window size, the difficulty should be the same as genesis'")
			}
		}
		for i := uint64(0); i < params.DifficultyAdjustmentWindowSize+100; i++ {
			tip, tipHash = addBlock(0, tipHash)
			if tip.Header.Bits != params.GenesisBlock.Header.Bits {
				t.Fatalf("As long as the block rate remains the same, the difficulty shouldn't change")
			}
		}

		blockInThePast, tipHash := addBlockWithMinimumTime(tipHash)
		if blockInThePast.Header.Bits != tip.Header.Bits {
			t.Fatalf("The difficulty should only change when blockInThePast is in the past of a block bluest parent")
		}
		tip = blockInThePast

		tip, tipHash = addBlock(0, tipHash)
		if tip.Header.Bits != blockInThePast.Header.Bits {
			t.Fatalf("The difficulty should only change when blockInThePast is in the past of a block bluest parent")
		}

		tip, tipHash = addBlock(0, tipHash)
		if compareBits(tip.Header.Bits, blockInThePast.Header.Bits) >= 0 {
			t.Fatalf("tip.bits should be smaller than blockInThePast.bits because blockInThePast increased the " +
				"block rate, so the difficulty should increase as well")
		}

		var expectedBits uint32
		switch params.Name {
		case "kaspa-testnet", "kaspa-devnet":
			expectedBits = uint32(0x1e7f83df)
		case "kaspa-mainnet", "kaspa-simnet":
			expectedBits = uint32(0x207f83df)
		}

		if tip.Header.Bits != expectedBits {
			t.Errorf("tip.bits was expected to be %x but got %x", expectedBits, tip.Header.Bits)
		}

		// Increase block rate to increase difficulty
		for i := uint64(0); i < params.DifficultyAdjustmentWindowSize; i++ {
			tip, tipHash = addBlockWithMinimumTime(tipHash)
			tipGHOSTDAGData, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), tipHash)
			if err != nil {
				t.Fatalf("GHOSTDAGDataStore: %+v", err)
			}

			selectedParentHeader, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(),
				tipGHOSTDAGData.SelectedParent)
			if err != nil {
				t.Fatalf("BlockHeader: %+v", err)
			}

			if compareBits(tip.Header.Bits, selectedParentHeader.Bits) > 0 {
				t.Fatalf("Because we're increasing the block rate, the difficulty can't decrease")
			}
		}

		// Add blocks until difficulty stabilizes
		lastBits := tip.Header.Bits
		sameBitsCount := uint64(0)
		for sameBitsCount < params.DifficultyAdjustmentWindowSize+1 {
			tip, tipHash = addBlock(0, tipHash)
			if tip.Header.Bits == lastBits {
				sameBitsCount++
			} else {
				lastBits = tip.Header.Bits
				sameBitsCount = 0
			}
		}

		slowBlockTime := tip.Header.TimeInMilliseconds + params.TargetTimePerBlock.Milliseconds() + 1000
		slowBlock, tipHash := addBlock(slowBlockTime, tipHash)
		if slowBlock.Header.Bits != tip.Header.Bits {
			t.Fatalf("The difficulty should only change when slowBlock is in the past of a block bluest parent")
		}

		tip = slowBlock

		tip, tipHash = addBlock(0, tipHash)
		if tip.Header.Bits != slowBlock.Header.Bits {
			t.Fatalf("The difficulty should only change when slowBlock is in the past of a block bluest parent")
		}
		tip, tipHash = addBlock(0, tipHash)
		if compareBits(tip.Header.Bits, slowBlock.Header.Bits) <= 0 {
			t.Fatalf("tip.bits should be smaller than slowBlock.bits because slowBlock decreased the block" +
				" rate, so the difficulty should decrease as well")
		}

		_, tipHash = addBlock(0, tipHash)
		splitBlockHash := tipHash
		for i := 0; i < 100; i++ {
			_, tipHash = addBlock(0, tipHash)
		}
		blueTipHash := tipHash

		redChainTipHash := splitBlockHash
		for i := 0; i < 10; i++ {
			_, redChainTipHash = addBlockWithMinimumTime(redChainTipHash)
		}
		tipWithRedPast, _ := addBlock(0, redChainTipHash, blueTipHash)
		tipWithoutRedPast, _ := addBlock(0, blueTipHash)
		if tipWithoutRedPast.Header.Bits != tipWithRedPast.Header.Bits {
			t.Fatalf("tipWithoutRedPast.bits should be the same as tipWithRedPast.bits because red blocks" +
				" shouldn't affect the difficulty")
		}
	})
}

func compareBits(a uint32, b uint32) int {
	aTarget := util.CompactToBig(a)
	bTarget := util.CompactToBig(b)
	return aTarget.Cmp(bTarget)
}
