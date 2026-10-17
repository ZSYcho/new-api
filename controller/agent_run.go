package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// GetAgentRunCost reports the token usage and cost of one agent run, per
// graph node, from the consume logs of the calling API token. Agents tag their
// calls with the X-Agent-Run-Id and X-Agent-Node request headers.
func GetAgentRunCost(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		common.ApiErrorI18n(c, i18n.MsgTokenInvalid)
		return
	}
	runId := c.Param("id")
	if !model.IsValidAgentRunId(runId) {
		common.ApiErrorI18n(c, i18n.MsgAgentRunInvalidId)
		return
	}
	cost, err := model.GetAgentRunCost(tokenId, runId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, cost)
}
