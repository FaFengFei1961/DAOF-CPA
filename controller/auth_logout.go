package controller

import (
	"fmt"
	"strings"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

// AuthLogout revokes the current browser session and evicts the legacy bearer token cache path.
func AuthLogout(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": err.Error()})
	}

	sessionID, _ := c.Locals("session_id").(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = extractBearerToken(c.Get("Authorization"))
	}
	if database.IsSessionID(sessionID) {
		if err := database.RevokeSessionByID(sessionID); err != nil {
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
		}
	}

	if user.Token != "" {
		proxy.EvictUserToken(user.Token)
	}

	LogOperationBy(user.ID, user.ID, "user", "USER_LOGOUT", c.IP(),
		fmt.Sprintf(`[{"type":"USER_LOGOUT","session":%q}]`, sessionID))

	return c.JSON(fiber.Map{"success": true})
}

func extractBearerToken(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
