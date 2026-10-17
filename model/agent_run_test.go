package model

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recordAgentCall(t *testing.T, tokenId int, headers map[string]string, params RecordConsumeLogParams) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	for key, value := range headers {
		c.Request.Header.Set(key, value)
	}
	params.TokenId = tokenId
	RecordConsumeLog(c, 1, params)
}

func TestRecordConsumeLogStoresAgentHeaders(t *testing.T) {
	require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)

	tests := []struct {
		name    string
		headers map[string]string
		other   *LogOther
		want    map[string]any
	}{
		{
			name:    "run and node",
			headers: map[string]string{AgentRunIdHeader: "run-1", AgentNodeHeader: "reflection"},
			other:   NewLogOther(),
			want:    map[string]any{"agent_run_id": "run-1", "agent_node": "reflection"},
		},
		{
			name:    "nil other is created",
			headers: map[string]string{AgentRunIdHeader: "run-2"},
			want:    map[string]any{"agent_run_id": "run-2"},
		},
		{
			name:    "malformed node is dropped",
			headers: map[string]string{AgentRunIdHeader: "run-3", AgentNodeHeader: "a b"},
			other:   NewLogOther(),
			want:    map[string]any{"agent_run_id": "run-3"},
		},
		{
			name:    "malformed run id records nothing",
			headers: map[string]string{AgentRunIdHeader: `x","admin_info":{"a":1}`, AgentNodeHeader: "reflection"},
			other:   NewLogOther(),
			want:    map[string]any{},
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordAgentCall(t, 100+i, tt.headers, RecordConsumeLogParams{ModelName: "m", Other: tt.other})
			var log Log
			require.NoError(t, LOG_DB.Where("token_id = ?", 100+i).First(&log).Error)
			got := map[string]any{}
			if log.Other != "" {
				require.NoError(t, common.UnmarshalJsonStr(log.Other, &got))
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetAgentRunCostGroupsByNodeForOneToken(t *testing.T) {
	require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	const tokenId = 7
	call := func(tokenId int, runId, node, modelName string, prompt, completion, quota int) {
		headers := map[string]string{AgentRunIdHeader: runId}
		if node != "" {
			headers[AgentNodeHeader] = node
		}
		recordAgentCall(t, tokenId, headers, RecordConsumeLogParams{
			ModelName: modelName, PromptTokens: prompt, CompletionTokens: completion, Quota: quota, Other: NewLogOther(),
		})
	}
	call(tokenId, "run_1", "generate_query", "qwen", 100, 20, 0)
	call(tokenId, "run_1", "kb_retrieve", "bge-m3", 12, 0, 0)
	call(tokenId, "run_1", "kb_retrieve", "bge-m3", 8, 0, 0)
	call(tokenId, "run_1", "finalize_answer", "deepseek-chat", 1000, 300, 150)
	call(tokenId, "run_1", "", "deepseek-chat", 10, 10, 5)
	// "_" is a LIKE wildcard: run_1 must not pick up runX1.
	call(tokenId, "runX1", "finalize_answer", "deepseek-chat", 999, 999, 999)
	// Another token's calls with the same run id belong to someone else.
	call(tokenId+1, "run_1", "finalize_answer", "deepseek-chat", 999, 999, 999)
	// Error logs carry the same metadata but cost nothing.
	require.NoError(t, LOG_DB.Create(&Log{
		TokenId: tokenId, Type: LogTypeError, Other: `{"agent_run_id":"run_1","agent_node":"reflection"}`,
	}).Error)

	cost, err := GetAgentRunCost(tokenId, "run_1")
	require.NoError(t, err)

	assert.Equal(t, "run_1", cost.RunId)
	assert.Equal(t, 5, cost.Calls)
	assert.Equal(t, 1130, cost.PromptTokens)
	assert.Equal(t, 330, cost.CompletionTokens)
	assert.Equal(t, 155, cost.Quota)
	assert.InDelta(t, 155/common.QuotaPerUnit, cost.CostUSD, 1e-12)

	require.Len(t, cost.Nodes, 4)
	assert.Equal(t, AgentNodeCost{
		Node: "finalize_answer", Calls: 1, PromptTokens: 1000, CompletionTokens: 300, Quota: 150,
		CostUSD: 150 / common.QuotaPerUnit, Models: []string{"deepseek-chat"},
	}, *cost.Nodes[0])
	assert.Equal(t, "", cost.Nodes[1].Node)
	assert.Equal(t, []string{"generate_query", "kb_retrieve"}, []string{cost.Nodes[2].Node, cost.Nodes[3].Node})
	assert.Equal(t, 2, cost.Nodes[3].Calls)
	assert.Equal(t, 20, cost.Nodes[3].PromptTokens)

	empty, err := GetAgentRunCost(tokenId, "missing")
	require.NoError(t, err)
	assert.Zero(t, empty.Calls)
	assert.Empty(t, empty.Nodes)
}
