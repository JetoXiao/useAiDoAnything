package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SecondLevelAgencyHandler struct{ affiliateService *service.AffiliateService }

func NewSecondLevelAgencyHandler(affiliateService *service.AffiliateService) *SecondLevelAgencyHandler {
	return &SecondLevelAgencyHandler{affiliateService: affiliateService}
}

func currentSubject(c *gin.Context) (int64, bool) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	return subject.UserID, ok && subject.UserID > 0
}

func (h *SecondLevelAgencyHandler) Status(c *gin.Context) {
	uid, ok := currentSubject(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	out, err := h.affiliateService.GetSecondLevelAgencyCapability(c.Request.Context(), uid)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}
func (h *SecondLevelAgencyHandler) List(c *gin.Context) {
	uid, ok := currentSubject(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	out, err := h.affiliateService.ListSecondLevelAgents(c.Request.Context(), uid)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

func (h *SecondLevelAgencyHandler) Candidates(c *gin.Context) {
	uid, ok := currentSubject(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	items, err := h.affiliateService.ListSecondLevelAgencyCandidates(c.Request.Context(), uid, c.Query("q"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

type createSecondLevelAgentRequest struct {
	UserID         int64   `json:"user_id"`
	AffCode        string  `json:"aff_code"`
	CommissionRate float64 `json:"commission_rate"`
}

func (h *SecondLevelAgencyHandler) Create(c *gin.Context) {
	uid, ok := currentSubject(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req createSecondLevelAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	out, err := h.affiliateService.CreateSecondLevelAgent(c.Request.Context(), uid, service.SecondLevelAgent{SubagentUserID: req.UserID, AffCode: req.AffCode, CommissionRate: req.CommissionRate})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}
func (h *SecondLevelAgencyHandler) SetStatus(c *gin.Context) {
	uid, ok := currentSubject(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.affiliateService.SetSecondLevelAgentStatus(c.Request.Context(), uid, id, req.Status); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"status": req.Status})
}

func (h *SecondLevelAgencyHandler) SetRate(c *gin.Context) {
	uid, ok := currentSubject(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	var req struct {
		CommissionRate float64 `json:"commission_rate"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.affiliateService.SetSecondLevelAgentCommissionRate(c.Request.Context(), uid, id, req.CommissionRate); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"commission_rate": req.CommissionRate})
}

func (h *SecondLevelAgencyHandler) Usage(c *gin.Context) {
	uid, ok := currentSubject(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	page, pageSize := response.ParsePagination(c)
	filter := service.AffiliateUsageFilter{Page: page, PageSize: pageSize, Search: c.Query("search"), View: c.Query("view"), SortBy: c.Query("sort_by"), SortDesc: c.Query("sort_order") != "asc"}
	items, summary, total, err := h.affiliateService.ListSecondLevelUsage(c.Request.Context(), uid, id, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "summary": summary, "total": total, "page": page, "page_size": pageSize})
}

func (h *SecondLevelAgencyHandler) Rebates(c *gin.Context) {
	uid, ok := currentSubject(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.affiliateService.ListSecondLevelRebates(c.Request.Context(), uid, id, service.AffiliateRecordFilter{Page: page, PageSize: pageSize, Search: c.Query("search")})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}
