package controller

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOutboundConnectionsRejectsMissingOrNullArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{}`, `{"outbound":"proxy"}`, `{"touches":null}`, `{"touches":{}}`, `{"touches":"all"}`} {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest("PUT", "/api/outboundConnections", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		PutOutboundConnections(ctx)
		if !strings.Contains(response.Body.String(), "touches must be an array") {
			t.Fatalf("invalid body %s reached service/database: %s", body, response.Body.String())
		}
	}
}
