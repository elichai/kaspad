// Copyright (c) 2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockdag

import (
	"github.com/kaspanet/kaspad/dagconfig"
	"github.com/kaspanet/kaspad/util"
	"reflect"
	"testing"
)

func buildNode(t *testing.T, dag *BlockDAG, parents BlockSet) *blockNode {
	block, err := PrepareBlockForTest(dag, parents.Hashes(), nil)
	if err != nil {
		t.Fatalf("error in PrepareBlockForTest: %s", err)
	}
	utilBlock := util.NewBlock(block)
	isOrphan, isDelayed, err := dag.ProcessBlock(utilBlock, BFNoPoWCheck)
	if err != nil {
		t.Fatalf("unexpected error in ProcessBlock: %s", err)
	}
	if isDelayed {
		t.Fatalf("block is too far in the future")
	}
	if isOrphan {
		t.Fatalf("block was unexpectedly orphan")
	}
	return nodeByMsgBlock(t, dag, block)
}

// TestVirtualBlock ensures that VirtualBlock works as expected.
func TestVirtualBlock(t *testing.T) {
	// Create a new database and DAG instance to run tests against.
	params := dagconfig.SimnetParams
	params.K = 1
	dag, teardownFunc, err := DAGSetup("TestVirtualBlock", true, Config{
		DAGParams: &params,
	})
	if err != nil {
		t.Fatalf("TestVirtualBlock: Failed to setup DAG instance: %s", err)
	}
	defer teardownFunc()

	resetExtraNonceForTest()

	// Create a DAG as follows:
	// 0 <- 1 <- 2
	//  \
	//   <- 3 <- 5
	//  \    X
	//   <- 4 <- 6
	node0 := dag.genesis
	node1 := buildNode(t, dag, BlockSetFromSlice(node0))
	node2 := buildNode(t, dag, BlockSetFromSlice(node1))
	node3 := buildNode(t, dag, BlockSetFromSlice(node0))
	node4 := buildNode(t, dag, BlockSetFromSlice(node0))
	node5 := buildNode(t, dag, BlockSetFromSlice(node3, node4))
	node6 := buildNode(t, dag, BlockSetFromSlice(node3, node4))

	// Given an empty VirtualBlock, each of the following test cases will:
	// Set its tips to tipsToSet
	// Add to it all the tips in tipsToAdd, one after the other
	// Call .Tips() on it and compare the result to expectedTips
	// Call .selectedTip() on it and compare the result to expectedSelectedParent
	tests := []struct {
		name                   string
		tipsToSet              []*blockNode
		tipsToAdd              []*blockNode
		expectedTips           BlockSet
		expectedSelectedParent *blockNode
	}{
		{
			name:                   "empty virtual",
			tipsToSet:              []*blockNode{},
			tipsToAdd:              []*blockNode{},
			expectedTips:           NewBlockSet(),
			expectedSelectedParent: nil,
		},
		{
			name:                   "virtual with genesis tip",
			tipsToSet:              []*blockNode{node0},
			tipsToAdd:              []*blockNode{},
			expectedTips:           BlockSetFromSlice(node0),
			expectedSelectedParent: node0,
		},
		{
			name:                   "virtual with genesis tip, add child of genesis",
			tipsToSet:              []*blockNode{node0},
			tipsToAdd:              []*blockNode{node1},
			expectedTips:           BlockSetFromSlice(node1),
			expectedSelectedParent: node1,
		},
		{
			name:                   "empty virtual, add a full DAG",
			tipsToSet:              []*blockNode{},
			tipsToAdd:              []*blockNode{node0, node1, node2, node3, node4, node5, node6},
			expectedTips:           BlockSetFromSlice(node2, node5, node6),
			expectedSelectedParent: node5,
		},
	}

	for _, test := range tests {
		// Create an empty VirtualBlock
		virtual := newVirtualBlock(dag, nil)

		// Set the tips. This will be the initial state
		virtual.SetTips(BlockSetFromSlice(test.tipsToSet...))

		// Add all blockNodes in tipsToAdd in order
		for _, tipToAdd := range test.tipsToAdd {
			addNodeAsChildToParents(tipToAdd)
			virtual.AddTip(tipToAdd)
		}

		// Ensure that the virtual block's tips are now equal to expectedTips
		resultTips := virtual.tips()
		if !reflect.DeepEqual(resultTips, test.expectedTips) {
			t.Errorf("unexpected tips in test \"%s\". "+
				"Expected: %v, got: %v.", test.name, test.expectedTips, resultTips)
		}

		// Ensure that the virtual block's selectedParent is now equal to expectedSelectedParent
		resultSelectedTip := virtual.selectedParent
		if !reflect.DeepEqual(resultSelectedTip, test.expectedSelectedParent) {
			t.Errorf("unexpected selected tip in test \"%s\". "+
				"Expected: %v, got: %v.", test.name, test.expectedSelectedParent, resultSelectedTip)
		}
	}
}

func TestSelectedPath(t *testing.T) {
	// Create a new database and DAG instance to run tests against.
	params := dagconfig.SimnetParams
	params.K = 1
	dag, teardownFunc, err := DAGSetup("TestSelectedPath", true, Config{
		DAGParams: &params,
	})
	if err != nil {
		t.Fatalf("TestSelectedPath: Failed to setup DAG instance: %s", err)
	}
	defer teardownFunc()

	// Create an empty VirtualBlock
	virtual := newVirtualBlock(dag, nil)

	tip := dag.genesis
	virtual.AddTip(tip)
	initialPath := BlockSetFromSlice(tip)
	for i := 0; i < 5; i++ {
		tip = buildNode(t, dag, BlockSetFromSlice(tip))
		initialPath.Add(tip)
		virtual.AddTip(tip)
	}
	initialTip := tip

	firstPath := initialPath.Clone()
	for i := 0; i < 5; i++ {
		tip = buildNode(t, dag, BlockSetFromSlice(tip))
		firstPath.Add(tip)
		virtual.AddTip(tip)
	}
	// For now we don't have any DAG, just chain, the selected path should include all the blocks on the chain.
	if !reflect.DeepEqual(virtual.selectedParentChainSet, firstPath) {
		t.Fatalf("TestSelectedPath: selectedPathSet doesn't include the expected values. got %v, want %v", virtual.selectedParent, firstPath)
	}
	// We expect that selectedParentChainSlice should have all the blocks we've added so far
	wantLen := 11
	gotLen := len(virtual.selectedParentChainSlice)
	if wantLen != gotLen {
		t.Fatalf("TestSelectedPath: selectedParentChainSlice doesn't have the expected length. got %d, want %d", gotLen, wantLen)
	}

	secondPath := initialPath.Clone()
	tip = initialTip
	for i := 0; i < 100; i++ {
		tip = buildNode(t, dag, BlockSetFromSlice(tip))
		secondPath.Add(tip)
		virtual.AddTip(tip)
	}
	// Because we added a chain that is much longer than the previous chain, the selected path should be re-organized.
	if !reflect.DeepEqual(virtual.selectedParentChainSet, secondPath) {
		t.Fatalf("TestSelectedPath: selectedPathSet didn't handle the re-org as expected. got %v, want %v", virtual.selectedParent, firstPath)
	}
	// We expect that selectedParentChainSlice should have all the blocks we've added so far except the old chain
	wantLen = 106
	gotLen = len(virtual.selectedParentChainSlice)
	if wantLen != gotLen {
		t.Fatalf("TestSelectedPath: selectedParentChainSlice doesn't have"+
			"the expected length, possibly because it didn't handle the re-org as expected. got %d, want %d", gotLen, wantLen)
	}

	tip = initialTip
	for i := 0; i < 3; i++ {
		tip = buildNode(t, dag, BlockSetFromSlice(tip))
		virtual.AddTip(tip)
	}
	// Because we added a very short chain, the selected path should not be affected.
	if !reflect.DeepEqual(virtual.selectedParentChainSet, secondPath) {
		t.Fatalf("TestSelectedPath: selectedPathSet did an unexpected re-org. got %v, want %v", virtual.selectedParent, firstPath)
	}
	// We expect that selectedParentChainSlice not to change
	wantLen = 106
	gotLen = len(virtual.selectedParentChainSlice)
	if wantLen != gotLen {
		t.Fatalf("TestSelectedPath: selectedParentChainSlice doesn't"+
			"have the expected length, possibly due to unexpected did an unexpected re-org. got %d, want %d", gotLen, wantLen)
	}

	// We call updateSelectedParentSet manually without updating the tips, to check if it panics
	virtual2 := newVirtualBlock(dag, nil)
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("updateSelectedParentSet didn't panic")
		}
	}()
	virtual2.updateSelectedParentSet(buildNode(t, dag, BlockSetFromSlice()))
}

func TestChainUpdates(t *testing.T) {
	// Create a new database and DAG instance to run tests against.
	params := dagconfig.SimnetParams
	params.K = 1
	dag, teardownFunc, err := DAGSetup("TestChainUpdates", true, Config{
		DAGParams: &params,
	})
	if err != nil {
		t.Fatalf("TestChainUpdates: Failed to setup DAG instance: %s", err)
	}
	defer teardownFunc()

	genesis := dag.genesis

	// Create a chain to be removed
	var toBeRemovedNodes []*blockNode
	toBeRemovedTip := genesis
	for i := 0; i < 5; i++ {
		toBeRemovedTip = buildNode(t, dag, BlockSetFromSlice(toBeRemovedTip))
		toBeRemovedNodes = append(toBeRemovedNodes, toBeRemovedTip)
	}

	// Create a VirtualBlock with the toBeRemoved chain
	virtual := newVirtualBlock(dag, BlockSetFromSlice(toBeRemovedNodes...))

	// Create a chain to be added
	var toBeAddedNodes []*blockNode
	toBeAddedTip := genesis
	for i := 0; i < 8; i++ {
		toBeAddedTip = buildNode(t, dag, BlockSetFromSlice(toBeAddedTip))
		toBeAddedNodes = append(toBeAddedNodes, toBeAddedTip)
	}

	// Set the virtual tip to be the tip of the toBeAdded chain
	chainUpdates := virtual.setTips(BlockSetFromSlice(toBeAddedTip))

	// Make sure that the removed blocks are as expected (in reverse order)
	if len(chainUpdates.removedChainBlockHashes) != len(toBeRemovedNodes) {
		t.Fatalf("TestChainUpdates: wrong removed amount. "+
			"Got: %d, want: %d", len(chainUpdates.removedChainBlockHashes), len(toBeRemovedNodes))
	}
	for i, removedHash := range chainUpdates.removedChainBlockHashes {
		correspondingRemovedNode := toBeRemovedNodes[len(toBeRemovedNodes)-1-i]
		if !removedHash.IsEqual(correspondingRemovedNode.hash) {
			t.Fatalf("TestChainUpdates: wrong removed hash. "+
				"Got: %s, want: %s", removedHash, correspondingRemovedNode.hash)
		}
	}

	// Make sure that the added blocks are as expected (in forward order)
	if len(chainUpdates.addedChainBlockHashes) != len(toBeAddedNodes) {
		t.Fatalf("TestChainUpdates: wrong added amount. "+
			"Got: %d, want: %d", len(chainUpdates.removedChainBlockHashes), len(toBeAddedNodes))
	}
	for i, addedHash := range chainUpdates.addedChainBlockHashes {
		correspondingAddedNode := toBeAddedNodes[i]
		if !addedHash.IsEqual(correspondingAddedNode.hash) {
			t.Fatalf("TestChainUpdates: wrong added hash. "+
				"Got: %s, want: %s", addedHash, correspondingAddedNode.hash)
		}
	}
}
