//
// COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
//
// This copyright notice is Copyright Management Information under 17 USC 1202
// and is included to protect this work and deter copyright infringement.
// Removal or alteration of this Copyright Management Information without the
// express written permission of Brightgate Inc is prohibited, and any
// such unauthorized removal or alteration will be a violation of federal law.
//

package main

import (
	"net/http"

	"bg/cloud_models/appliancedb"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo"
	"github.com/satori/uuid"
)

type orgHandler struct {
	db           appliancedb.DataStore
	sessionStore sessions.Store
}

type orgsResponse struct {
	OrganizationUUID uuid.UUID `json:"organizationUUID"`
	Name             string    `json:"name"`
	Relationship     string    `json:"relationship"`
}

func (o *orgHandler) getOrgs(c echo.Context) error {
	ctx := c.Request().Context()

	accountUUID, ok := c.Get("account_uuid").(uuid.UUID)
	if !ok || accountUUID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized)
	}
	acct, err := o.db.AccountByUUID(ctx, accountUUID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}
	rels, err := o.db.OrgOrgRelationshipsByOrg(ctx, acct.OrganizationUUID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}
	response := make([]orgsResponse, len(rels))
	for idx, rel := range rels {
		tgtOrg, err := o.db.OrganizationByUUID(ctx, rel.TargetOrganizationUUID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError)
		}
		response[idx] = orgsResponse{
			OrganizationUUID: tgtOrg.UUID,
			Name:             tgtOrg.Name,
			Relationship:     rel.Relationship,
		}
	}
	return c.JSON(http.StatusOK, response)
}

func (o *orgHandler) getOrgAccounts(c echo.Context) error {
	ctx := c.Request().Context()
	accountUUID, ok := c.Get("account_uuid").(uuid.UUID)
	if !ok || accountUUID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized)
	}

	orgUUID, err := uuid.FromString(c.Param("org_uuid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}

	accounts, err := o.db.AccountInfosByOrganization(ctx, orgUUID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err)
	} else if accounts == nil {
		// Return empty array, not null, to REST consumer
		accounts = make([]appliancedb.AccountInfo, 0)
	}
	return c.JSON(http.StatusOK, accounts)
}

// mkOrgMiddleware manufactures a middleware which protects a route; only
// users with one or more of the allowedRoles can pass through the checks; the
// middleware adds "matched_roles" to the echo context, indicating which of the
// allowed_roles the user actually has.
func (o *orgHandler) mkOrgMiddleware(allowedRoles []string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			accountUUID, ok := c.Get("account_uuid").(uuid.UUID)
			if !ok || accountUUID == uuid.Nil {
				return echo.NewHTTPError(http.StatusUnauthorized)
			}

			orgUUID, err := uuid.FromString(c.Param("org_uuid"))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest)
			}
			aoRoles, err := o.db.AccountOrgRolesByAccountTarget(ctx,
				accountUUID, orgUUID)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError)
			}
			matches := make(matchedRoles)
			for _, aor := range aoRoles {
				for _, r := range aor.Roles {
					for _, rr := range allowedRoles {
						if r == rr {
							matches[r] = true
						}
					}
				}
			}
			if len(matches) > 0 {
				c.Set("matched_roles", matches)
				return next(c)
			}
			c.Logger().Debugf("Unauthorized: %s org=%v, acc=%v, ur=%v, ar=%v",
				c.Path(), orgUUID, accountUUID, aoRoles, allowedRoles)
			return echo.NewHTTPError(http.StatusUnauthorized)
		}
	}
}

// newOrgAPIHandler creates an orgHandler for the given DataStore and session
// Store, and routes the handler into the echo instance.
func newOrgHandler(r *echo.Echo, db appliancedb.DataStore, middlewares []echo.MiddlewareFunc, sessionStore sessions.Store) *orgHandler {
	h := &orgHandler{db, sessionStore}
	r.GET("/api/org", h.getOrgs, middlewares...)

	admin := h.mkOrgMiddleware([]string{"admin"})

	org := r.Group("/api/org/:org_uuid")
	org.Use(middlewares...)
	org.GET("/accounts", h.getOrgAccounts, admin)
	return h
}
