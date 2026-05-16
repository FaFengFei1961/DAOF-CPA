package controller

import (
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

// GetPublicBillingRules exposes the public, auditable charging rules used to
// turn raw API-equivalent cost into subscription credits.
func GetPublicBillingRules(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"success": true,
		"data":    proxy.GetPublicBillingRules(),
	})
}
