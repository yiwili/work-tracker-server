package publish

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSameBatchContent(t *testing.T) {
	input := RegisterBatchInput{
		IdempotencyKey: "test-key-1",
		Executor:       "device",
		Items: []PublishItemInput{
			{
				WorkItemID:      "item-1",
				WorkItemVersion: 1,
				TargetScope:     "scope-1",
				Operation:       "create",
				Target:          map[string]any{"repo": "myrepo"},
				Payload:         map[string]any{"title": "Test Title"},
			},
		},
	}

	batch := PublishBatch{
		IdempotencyKey: "test-key-1",
		Executor:       "device",
		Requests: []PublishRequest{
			{
				WorkItemID:      "item-1",
				WorkItemVersion: 1,
				TargetScope:     "scope-1",
				Operation:       "create",
				Target:          map[string]any{"repo": "myrepo"},
				Payload:         map[string]any{"title": "Test Title"},
			},
		},
	}

	// 1. Identical payload
	require.True(t, isSameBatchContent(input, batch))

	// 2. Different executor
	inputDiffExec := input
	inputDiffExec.Executor = "cloud"
	require.False(t, isSameBatchContent(inputDiffExec, batch))

	// 3. Different items count
	inputMoreItems := input
	inputMoreItems.Items = append(inputMoreItems.Items, PublishItemInput{
		WorkItemID:      "item-2",
		WorkItemVersion: 1,
		TargetScope:     "scope-2",
		Operation:       "create",
	})
	require.False(t, isSameBatchContent(inputMoreItems, batch))

	// 4. Different version
	inputDiffVer := input
	inputDiffVer.Items = []PublishItemInput{
		{
			WorkItemID:      "item-1",
			WorkItemVersion: 2,
			TargetScope:     "scope-1",
			Operation:       "create",
			Target:          map[string]any{"repo": "myrepo"},
			Payload:         map[string]any{"title": "Test Title"},
		},
	}
	require.False(t, isSameBatchContent(inputDiffVer, batch))

	// 5. Different payload
	inputDiffPayload := input
	inputDiffPayload.Items = []PublishItemInput{
		{
			WorkItemID:      "item-1",
			WorkItemVersion: 1,
			TargetScope:     "scope-1",
			Operation:       "create",
			Target:          map[string]any{"repo": "myrepo"},
			Payload:         map[string]any{"title": "Different Title"},
		},
	}
	require.False(t, isSameBatchContent(inputDiffPayload, batch))

	// 6. Operation adjusted from create to update is acceptable
	batchAdjusted := batch
	batchAdjusted.Requests[0].Operation = "update"
	batchAdjusted.Requests[0].OperationAdjusted = true
	require.True(t, isSameBatchContent(input, batchAdjusted))
}
