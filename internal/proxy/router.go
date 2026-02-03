package proxy

import "github.com/gin-gonic/gin"

// NewRouter creates the gin router and wires the proxy handler.
func NewRouter(handler *Handler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Any("/*path", handler.Handle)

	return r
}
