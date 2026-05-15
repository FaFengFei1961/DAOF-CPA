package controller

import (
	"fmt"
	"strings"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
)

// AuthLogout revokes the current browser session. API keys are not revoked here;
// users must manage long-lived SDK credentials from the token manager.
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
