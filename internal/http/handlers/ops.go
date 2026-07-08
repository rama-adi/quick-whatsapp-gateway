package handlers

import "github.com/danielgtaylor/huma/v2"

// RegisterAllOps registers every code-first (huma) operation on the API. It is the
// single source of truth for which resources have been converted off the
// hand-mounted chi routes (mountAPIRoutes in internal/http) — both the live router
// and cmd/genopenapi call it, so the served routes and the generated spec can
// never diverge. As each resource is converted, its RegisterXOps is added here and
// removed from mountAPIRoutes.
func RegisterAllOps(api huma.API, h *Handlers) {
	RegisterWebhookOps(api, h)
	RegisterSessionOps(api, h)
	RegisterMessageOps(api, h)
	RegisterChatOps(api, h)
	RegisterContactOps(api, h)
	RegisterGroupOps(api, h)
	RegisterChannelOps(api, h)
	RegisterStatusOps(api, h)
	RegisterAdminOps(api, h)
	RegisterBackupOps(api, h)
	RegisterOAuthAppOps(api, h)
}
