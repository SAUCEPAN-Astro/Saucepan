package main

import (
	"net/http"

	"github.com/saucepan/hotpath/shared/cohort"
)

// The HTTP contract, in one place (#452). Before this, the route set lived only
// as ~65 inline mux.HandleFunc calls in main(), the SDK re-typed every path as a
// string literal, and openapi.yaml was hand-maintained separately — nothing
// diffed the three. apiRoutes is now the single Go-side source of truth:
//
//   - registerAPIRoutes wires it onto the mux (main()).
//   - rest_contract_test.go asserts it equals SaucepanServer/contracts/rest/routes.json
//     (drift either direction fails the build), and that every openapi.yaml path
//     resolves to a route here.
//   - The SDK's tests/test_rest_contract.py loads the same routes.json and
//     asserts every path literal it calls is declared here.
//
// Change procedure: edit apiRoutes → run the task-server tests (they rewrite
// nothing, they compare) → update routes.json to match → update openapi.yaml if
// the route is on the published developer surface → run the SDK tests.

// apiSurface groups a route by which consumer contract it belongs to. It is
// recorded in routes.json so each side can gate only the surface it owns.
type apiSurface string

const (
	// surfacePier — device/pier-facing (quest, uploads, auth devices). Contract
	// consumer is cmd/pier-agent + cmd/saucepan, not the SDK.
	surfacePier apiSurface = "pier"
	// surfaceAuth — identity (register/login/refresh/password). Shared.
	surfaceAuth apiSurface = "auth"
	// surfaceDeveloper — the API-key developer API published in openapi.yaml;
	// consumed by saucepan._http.Client.
	surfaceDeveloper apiSurface = "developer"
	// surfaceResearcher — first-party researcher SDK (campaigns, board, inbox,
	// events); consumed by saucepan.campaigns.CampaignClient. No openapi.yaml
	// coverage by design — routes.json is its contract.
	surfaceResearcher apiSurface = "researcher"
	// surfaceInfra — internal plumbing (worker queue, grades ingest, health).
	// No external consumer.
	surfaceInfra apiSurface = "infra"
)

type apiRoute struct {
	Method  string
	Path    string
	Surface apiSurface
	Handler http.HandlerFunc
}

// apiRoutes is every route the apiserver registers, except the two trivial
// health endpoints wired directly in registerAPIRoutes. Keep it grouped and
// ordered as the mux block was.
var apiRoutes = []apiRoute{
	// Telescope endpoints — JWT or device token required (#263).
	{"POST", "/quest/telescopes", surfacePier, requireDeviceOrJWT(handleRegisterTelescope)},
	{"PATCH", "/quest/telescopes/{id}", surfacePier, requireDeviceOrJWT(handleRegisterTelescope)},

	// Task endpoints — writes require approved researcher (#284).
	{"POST", "/quest/tasks", surfacePier, requireResearcherJWT(handleCreateTask)},
	{"GET", "/quest/tasks/{id}", surfacePier, handleGetTaskByID},
	{"PATCH", "/quest/tasks/{id}", surfacePier, requireResearcherJWT(handlePatchTask)},
	{"PATCH", "/quest/tasks/{id}/complete", surfacePier, requireResearcherJWT(handleCompleteTask)},

	// Handoff (Go-only — replaces Flask dev-server handoff routes)
	{"GET", "/quest/handoff-broadcast", surfacePier, handleHandoffBroadcast},
	{"GET", "/quest/handoff-status", surfacePier, handleHandoffStatus},
	{"POST", "/quest/tasks/{id}/emergency-handoff", surfacePier, requireDeviceOrJWT(handleEmergencyHandoff)},
	{"PATCH", "/quest/tasks/{id}/handoff", surfacePier, requireResearcherJWT(handleUpdateTaskHandoff)},

	// Auth: identity (register/login/refresh) proxied to user-server; devices stay here
	{"POST", "/auth/register", surfaceAuth, handleAuthRegister},
	{"POST", "/auth/verify-email", surfaceAuth, handleAuthVerifyEmail},
	{"POST", "/auth/login", surfaceAuth, handleAuthLogin},
	{"POST", "/auth/refresh", surfaceAuth, handleAuthRefresh},
	{"POST", "/auth/logout", surfaceAuth, handleAuthLogout},
	{"POST", "/auth/change-password", surfaceAuth, handleAuthChangePassword},
	{"POST", "/auth/forgot-password", surfaceAuth, handleAuthForgotPassword},
	{"POST", "/auth/reset-password", surfaceAuth, handleAuthResetPassword},
	{"POST", "/auth/devices", surfacePier, requireJWT(handleAuthDevicesCreate)},
	{"GET", "/auth/devices", surfacePier, requireJWT(handleAuthDevicesList)},
	{"DELETE", "/auth/devices/{node_id}", surfacePier, requireJWT(handleAuthDevicesDelete)},
	{"POST", "/auth/heartbeat", surfacePier, requireJWT(handleAuthHeartbeat)},
	// Legacy aliases (deprecated)
	{"POST", "/auth/verify", surfaceAuth, handleAuthVerifyDeprecated},
	{"POST", "/auth/reset-request", surfaceAuth, handleAuthResetRequestDeprecated},
	{"POST", "/auth/reset", surfaceAuth, handleAuthResetDeprecated},

	// Upload endpoints (multipart Cloudflare R2 → SDK)
	{"POST", "/upload/start", surfacePier, requireUploadDevice(handleUploadStart)},
	{"POST", "/upload/presign", surfacePier, requireUploadDevice(handleUploadPresign)},
	{"POST", "/upload/complete", surfacePier, requireUploadDevice(handleUploadComplete)},

	// Bridge worker pull queue (local / physical unit)
	{"GET", "/api/v1/worker/pending", surfaceInfra, handleWorkerPending},
	{"POST", "/api/v1/worker/enqueue", surfaceInfra, handleWorkerEnqueue},
	{"POST", "/api/v1/worker/stack-product", surfaceInfra, handleWorkerStackProduct},

	// Grades / points (datalake sync callback — Go-first, no Flask :5000)
	{"POST", "/api/v1/grades/ingest", surfaceInfra, handleGradesIngest},
	{"GET", "/api/v1/telescopes/{id}/points", surfaceInfra, handleTelescopePoints},

	// Campaign endpoints (researcher JWT; reads scoped to campaign owner)
	{"POST", "/api/v1/campaigns", surfaceResearcher, requireResearcherJWT(handleCreateCampaign)},
	{"GET", "/api/v1/campaigns", surfaceResearcher, requireResearcherJWT(handleListCampaigns)},
	{"GET", "/api/v1/campaigns/{id}", surfaceResearcher, requireResearcherJWT(handleGetCampaign)},
	{"POST", "/api/v1/campaigns/{id}/publish", surfaceResearcher, requireResearcherJWT(handlePublishCampaign)},
	{"POST", "/api/v1/campaigns/{id}/pause", surfaceResearcher, requireResearcherJWT(handlePauseCampaign)},
	{"POST", "/api/v1/campaigns/{id}/resume", surfaceResearcher, requireResearcherJWT(handleResumeCampaign)},
	{"GET", "/api/v1/campaigns/{id}/tasks", surfaceResearcher, requireResearcherJWT(handleListCampaignTasks)},
	{"POST", "/api/v1/campaigns/{id}/tasks", surfaceResearcher, requireResearcherJWT(handleAddCampaignTask)},
	{"GET", "/api/v1/campaigns/{id}/leaderboard", surfaceResearcher, requireResearcherJWT(handleCampaignLeaderboard)},
	{"GET", "/api/v1/campaigns/{id}/stack-status", surfaceResearcher, requireResearcherJWT(handleCampaignStackStatus)},
	{"POST", "/api/v1/campaigns/{id}/coverage", surfaceResearcher, requireResearcherJWT(handleSetCampaignCoverage)},
	{"POST", "/api/v1/campaigns/{id}/coverage/preview", surfaceResearcher, requireResearcherJWT(handlePreviewCampaignCoverage)},
	{"POST", "/api/v1/campaigns/{id}/coverage/apply", surfaceResearcher, requireResearcherJWT(handleApplyCampaignCoverage)},
	{"GET", "/api/v1/campaigns/{id}/coverage/status", surfaceResearcher, requireResearcherJWT(handleCampaignCoverageStatus)},
	// Campaign board (#179 / #331-C2) — researcher HTTP side of the campaign
	// messageboard; the pier side is the retained MQTT board (#463/#470).
	{"GET", "/api/v1/campaigns/{id}/board", surfaceResearcher, requireResearcherJWT(handleGetCampaignBoardNotes)},
	{"POST", "/api/v1/campaigns/{id}/board", surfaceResearcher, requireResearcherJWT(handlePostCampaignBoardNote)},
	{"GET", "/api/v1/fleet/sites", surfaceResearcher, requireResearcherJWT(handleFleetSites)},
	{"GET", "/api/v1/account/usage", surfaceResearcher, requireResearcherJWT(handleAccountUsage)},
	{"GET", "/api/v1/observation-groups/{id}", surfaceResearcher, requireResearcherJWT(handleGetObservationGroup)},

	// Developer API — published in SaucepanSDK/openapi.yaml.
	{"GET", "/api/v1/me", surfaceDeveloper, requireJWT(handleDeveloperMe)},
	{"GET", "/api/v1/me/quota", surfaceDeveloper, requireAPIKey(devScopeStatusRead)(handleDeveloperQuota)},
	{"GET", "/api/v1/keys", surfaceDeveloper, requireJWT(handleListAPIKeys)},
	{"POST", "/api/v1/keys", surfaceDeveloper, requireJWT(handleCreateAPIKey)},
	{"DELETE", "/api/v1/keys/{key_id}", surfaceDeveloper, requireJWT(handleRevokeAPIKey)},
	{"POST", "/api/v1/tasks", surfaceDeveloper, requireAPIKey(devScopeTasksWrite)(handleDeveloperCreateTask)},
	{"GET", "/api/v1/tasks", surfaceDeveloper, requireAPIKey(devScopeTasksRead)(handleDeveloperListTasks)},
	{"GET", "/api/v1/tasks/{task_id}", surfaceDeveloper, requireAPIKey(devScopeTasksRead)(handleDeveloperGetTask)},
	{"GET", "/api/v1/tasks/{task_id}/download-url", surfaceDeveloper, requireAPIKey(devScopeTasksRead)(handleDeveloperTaskDownloadURL)},

	// Inbox — shared between the developer Client and the researcher SDK.
	{"GET", "/api/v1/inbox", surfaceDeveloper, dispatchInboxPoll},
	{"POST", "/api/v1/inbox/{id}/ack", surfaceDeveloper, dispatchInboxAck},
	{"PATCH", "/api/v1/inbox/{notification_id}", surfaceDeveloper, dispatchInboxAck},

	// Researcher text events (alerts / updates) — saucepan.campaigns.TextInbox.
	{"GET", "/api/v1/alerts", surfaceResearcher, handleListResearcherEvents(eventKindAlert)},
	{"POST", "/api/v1/alerts/{id}/ack", surfaceResearcher, handleAckResearcherEvent(eventKindAlert)},
	{"GET", "/api/v1/updates", surfaceResearcher, handleListResearcherEvents(eventKindUpdate)},
	{"POST", "/api/v1/updates/{id}/ack", surfaceResearcher, handleAckResearcherEvent(eventKindUpdate)},
}

// registerAPIRoutes wires apiRoutes plus the two health endpoints onto mux.
func registerAPIRoutes(mux *http.ServeMux) {
	for _, r := range apiRoutes {
		mux.HandleFunc(r.Method+" "+r.Path, r.Handler)
	}

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{
			"status":  "healthy",
			"service": "saucepan-api",
		})
	})
	mux.HandleFunc("GET /cohort/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{
			"threshold":   cohortThreshold,
			"weights":     cohortWeights[:],
			"ndims":       cohort.NDims,
			"target_size": "3-8",
		})
	})
}
