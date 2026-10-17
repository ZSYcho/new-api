package model

import (
	"regexp"
	"sort"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
)

// Agent frameworks tag each model call with the run and graph node that issued
// it, so a multi-step agent run can be costed as a whole and per step.
const (
	AgentRunIdHeader = "X-Agent-Run-Id"
	AgentNodeHeader  = "X-Agent-Node"

	logOtherAgentRunIdKey = "agent_run_id"
	logOtherAgentNodeKey  = "agent_node"

	// agentRunMaxLogs bounds one cost query; a single agent run makes far
	// fewer model calls than this.
	agentRunMaxLogs = 5000
)

// Run ids and node names are stored in the log and matched with LIKE, so they
// are limited to characters that need no escaping in JSON or SQL patterns.
var (
	agentRunIdPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	agentNodePattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

func IsValidAgentRunId(runId string) bool {
	return agentRunIdPattern.MatchString(runId)
}

// appendAgentInfo copies the agent headers of the relayed request into the
// log metadata. Malformed values are dropped rather than stored.
func appendAgentInfo(c *gin.Context, other *LogOther) *LogOther {
	if c == nil || c.Request == nil {
		return other
	}
	runId := c.GetHeader(AgentRunIdHeader)
	if !IsValidAgentRunId(runId) {
		return other
	}
	if other == nil {
		other = NewLogOther()
	}
	other.SetPublic(logOtherAgentRunIdKey, runId)
	if node := c.GetHeader(AgentNodeHeader); agentNodePattern.MatchString(node) {
		other.SetPublic(logOtherAgentNodeKey, node)
	}
	return other
}

type AgentNodeCost struct {
	Node             string   `json:"node"`
	Calls            int      `json:"calls"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	Quota            int      `json:"quota"`
	CostUSD          float64  `json:"cost_usd"`
	UseTimeSeconds   int      `json:"use_time_seconds"`
	Models           []string `json:"models"`
}

type AgentRunCost struct {
	RunId            string           `json:"run_id"`
	Calls            int              `json:"calls"`
	PromptTokens     int              `json:"prompt_tokens"`
	CompletionTokens int              `json:"completion_tokens"`
	Quota            int              `json:"quota"`
	CostUSD          float64          `json:"cost_usd"`
	StartedAt        int64            `json:"started_at"`
	EndedAt          int64            `json:"ended_at"`
	Nodes            []*AgentNodeCost `json:"nodes"`
}

type agentRunLog struct {
	ModelName        string
	PromptTokens     int
	CompletionTokens int
	Quota            int
	UseTime          int
	CreatedAt        int64
	Other            string
}

// GetAgentRunCost sums the consume logs of one agent run made with tokenId,
// grouped by graph node. Calls without a node header are grouped under "".
func GetAgentRunCost(tokenId int, runId string) (*AgentRunCost, error) {
	// The run id is restricted to [A-Za-z0-9._:-], so the only LIKE wildcard
	// it can contain is "_"; buildLogLikeCondition escapes it per database.
	condition, pattern, err := buildLogLikeCondition("other",
		`%"`+logOtherAgentRunIdKey+`":"`+runId+`"%`)
	if err != nil {
		return nil, err
	}
	var rows []agentRunLog
	err = LOG_DB.Model(&Log{}).
		Select("model_name, prompt_tokens, completion_tokens, quota, use_time, created_at, other").
		Where("token_id = ? AND type = ?", tokenId, LogTypeConsume).
		Where(condition, pattern).
		Limit(agentRunMaxLogs).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	run := &AgentRunCost{RunId: runId, Nodes: []*AgentNodeCost{}}
	nodes := make(map[string]*AgentNodeCost)
	models := make(map[string]map[string]struct{})
	for _, row := range rows {
		var other map[string]any
		if err := common.UnmarshalJsonStr(row.Other, &other); err != nil {
			continue
		}
		// The LIKE match is a prefilter; confirm the exact run id here.
		if id, _ := other[logOtherAgentRunIdKey].(string); id != runId {
			continue
		}
		name, _ := other[logOtherAgentNodeKey].(string)
		node, ok := nodes[name]
		if !ok {
			node = &AgentNodeCost{Node: name}
			nodes[name] = node
			models[name] = make(map[string]struct{})
		}
		node.Calls++
		node.PromptTokens += row.PromptTokens
		node.CompletionTokens += row.CompletionTokens
		node.Quota += row.Quota
		node.UseTimeSeconds += row.UseTime
		models[name][row.ModelName] = struct{}{}

		run.Calls++
		run.PromptTokens += row.PromptTokens
		run.CompletionTokens += row.CompletionTokens
		run.Quota += row.Quota
		if run.StartedAt == 0 || row.CreatedAt < run.StartedAt {
			run.StartedAt = row.CreatedAt
		}
		if row.CreatedAt > run.EndedAt {
			run.EndedAt = row.CreatedAt
		}
	}

	for name, node := range nodes {
		node.CostUSD = float64(node.Quota) / common.QuotaPerUnit
		for modelName := range models[name] {
			node.Models = append(node.Models, modelName)
		}
		sort.Strings(node.Models)
		run.Nodes = append(run.Nodes, node)
	}
	// Most expensive node first; ties keep a stable order by name.
	sort.Slice(run.Nodes, func(i, j int) bool {
		if run.Nodes[i].Quota != run.Nodes[j].Quota {
			return run.Nodes[i].Quota > run.Nodes[j].Quota
		}
		return run.Nodes[i].Node < run.Nodes[j].Node
	})
	run.CostUSD = float64(run.Quota) / common.QuotaPerUnit
	return run, nil
}
