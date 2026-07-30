package iga

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

const scimUserSchema = "urn:ietf:params:scim:schemas:core:2.0:User"
const scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
const scimListSchema = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
const scimErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
const scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"

var userNameFilterRE = regexp.MustCompile(`(?i)^\s*userName\s+eq\s+"([^"]+)"\s*$`)

func ParseUserNameFilter(filter string) (string, bool) {
	if filter == "" {
		return "", true
	}
	m := userNameFilterRE.FindStringSubmatch(filter)
	if len(m) != 2 {
		return "", false
	}
	return m[1], true
}

type scimResource map[string]any

func scimErr(w http.ResponseWriter, status int, typ, detail string) {
	api.WriteJSON(w, status, scimResource{"schemas": []string{scimErrorSchema}, "status": strconv.Itoa(status), "scimType": typ, "detail": detail})
}
func scimMeta(kind, id string, t time.Time) scimResource {
	return scimResource{"resourceType": kind, "location": "/scim/v2/" + kind + "s/" + id, "created": t, "lastModified": t}
}
func scimUser(u user.User) scimResource {
	return scimResource{"schemas": []string{scimUserSchema}, "id": u.ID, "userName": u.Email, "externalId": u.ExternalID, "displayName": u.DisplayName, "active": u.Status != "inactive", "emails": []scimResource{{"value": u.Email, "primary": true}}, "meta": scimMeta("User", u.ID, u.UpdatedAt)}
}
func scimGroup(g user.Team, members []user.TeamMember) scimResource {
	ms := []scimResource{}
	for _, m := range members {
		ms = append(ms, scimResource{"value": m.UserID})
	}
	return scimResource{"schemas": []string{scimGroupSchema}, "id": g.ID, "displayName": g.Name, "members": ms, "meta": scimMeta("Group", g.ID, g.UpdatedAt)}
}

func (h *Handler) ServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, scimResource{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"}, "patch": scimResource{"supported": true}, "filter": scimResource{"supported": true, "maxResults": 100}, "bulk": scimResource{"supported": false}, "changePassword": scimResource{"supported": false}, "sort": scimResource{"supported": false}, "etag": scimResource{"supported": false}, "authenticationSchemes": []scimResource{{"type": "oauthbearertoken", "name": "Bearer"}}})
}
func scimPaging(r *http.Request) (int, int) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if start < 1 {
		start = 1
	}
	if count < 1 || count > 100 {
		count = 100
	}
	return start, count
}
func (h *Handler) SCIMListUsers(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	start, count := scimPaging(r)
	filter, ok := ParseUserNameFilter(r.URL.Query().Get("filter"))
	if !ok {
		scimErr(w, 400, "invalidFilter", "only userName eq filters are supported")
		return
	}
	users, total, err := h.users.ListUsers(t.OrganizationID, filter, api.PaginationParams{Limit: count, Offset: start - 1})
	if err != nil {
		scimErr(w, 500, "", err.Error())
		return
	}
	res := []scimResource{}
	for _, u := range users {
		if filter == "" || strings.EqualFold(u.Email, filter) {
			res = append(res, scimUser(u))
		}
	}
	if filter != "" {
		total = len(res)
	}
	api.WriteJSON(w, 200, scimResource{"schemas": []string{scimListSchema}, "totalResults": total, "startIndex": start, "itemsPerPage": len(res), "Resources": res})
}
func (h *Handler) SCIMGetUser(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	u, err := h.users.GetUser(t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		scimErr(w, 404, "", "user not found")
		return
	}
	api.WriteJSON(w, 200, scimUser(*u))
}
func readSCIM(r *http.Request) (map[string]any, error) {
	var v map[string]any
	err := api.ReadJSON(r, &v)
	return v, err
}
func (h *Handler) SCIMCreateUser(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	body, err := readSCIM(r)
	if err != nil {
		scimErr(w, 400, "invalidSyntax", err.Error())
		return
	}
	email := fmt.Sprint(body["userName"])
	display := fmt.Sprint(body["displayName"])
	active := true
	if v, ok := body["active"].(bool); ok {
		active = v
	}
	if email == "" || display == "" {
		scimErr(w, 400, "invalidValue", "userName and displayName are required")
		return
	}
	status := "active"
	if !active {
		status = "inactive"
	}
	u := &user.User{OrganizationID: t.OrganizationID, Email: email, DisplayName: display, Status: status}
	if v, ok := body["externalId"].(string); ok {
		u.ExternalID = v
	}
	if err := h.users.CreateUser(u); err != nil {
		scimErr(w, 500, "", err.Error())
		return
	}
	api.WriteJSON(w, 201, scimUser(*u))
}
func (h *Handler) SCIMPutUser(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	body, err := readSCIM(r)
	if err != nil {
		scimErr(w, 400, "invalidSyntax", err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	display := fmt.Sprint(body["displayName"])
	active := true
	if v, ok := body["active"].(bool); ok {
		active = v
	}
	status := "active"
	if !active {
		status = "inactive"
	}
	req := user.UpdateUserRequest{DisplayName: &display, Status: &status}
	u, err := h.users.UpdateUser(t.OrganizationID, id, req)
	if err != nil {
		scimErr(w, 404, "", "user not found")
		return
	}
	api.WriteJSON(w, 200, scimUser(*u))
}
func (h *Handler) SCIMPatchUser(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	body, err := readSCIM(r)
	if err != nil {
		scimErr(w, 400, "invalidSyntax", err.Error())
		return
	}
	req := user.UpdateUserRequest{}
	if ops, ok := body["Operations"].([]any); ok {
		for _, op := range ops {
			m, _ := op.(map[string]any)
			path := strings.ToLower(fmt.Sprint(m["path"]))
			if path == "active" {
				v := fmt.Sprint(m["value"])
				status := "inactive"
				if v == "true" {
					status = "active"
				}
				req.Status = &status
			}
			if path == "displayname" {
				v := fmt.Sprint(m["value"])
				req.DisplayName = &v
			}
		}
	}
	u, err := h.users.UpdateUser(t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		scimErr(w, 404, "", "user not found")
		return
	}
	api.WriteJSON(w, 200, scimUser(*u))
}
func (h *Handler) SCIMDeleteUser(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	status := "inactive"
	_, err := h.users.UpdateUser(t.OrganizationID, chi.URLParam(r, "id"), user.UpdateUserRequest{Status: &status})
	if err != nil {
		scimErr(w, 404, "", "user not found")
		return
	}
	w.WriteHeader(204)
}

func (h *Handler) SCIMListGroups(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	start, count := scimPaging(r)
	teams, total, err := h.users.ListTeams(t.OrganizationID, "", api.PaginationParams{Limit: count, Offset: start - 1})
	if err != nil {
		scimErr(w, 500, "", err.Error())
		return
	}
	res := []scimResource{}
	for _, g := range teams {
		members, _ := h.users.ListTeamMembers(t.OrganizationID, g.ID)
		res = append(res, scimGroup(g, members))
	}
	api.WriteJSON(w, 200, scimResource{"schemas": []string{scimListSchema}, "totalResults": total, "startIndex": start, "itemsPerPage": len(res), "Resources": res})
}
func (h *Handler) SCIMGetGroup(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	g, err := h.users.GetTeam(t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		scimErr(w, 404, "", "group not found")
		return
	}
	members, _ := h.users.ListTeamMembers(t.OrganizationID, g.ID)
	api.WriteJSON(w, 200, scimGroup(*g, members))
}
func (h *Handler) SCIMCreateGroup(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	body, err := readSCIM(r)
	if err != nil {
		scimErr(w, 400, "invalidSyntax", err.Error())
		return
	}
	name := fmt.Sprint(body["displayName"])
	g := &user.Team{OrganizationID: t.OrganizationID, Name: name}
	if name == "" {
		scimErr(w, 400, "invalidValue", "displayName is required")
		return
	}
	if err := h.users.CreateTeam(g); err != nil {
		scimErr(w, 500, "", err.Error())
		return
	}
	applyMembers(h, t.OrganizationID, g.ID, body)
	members, _ := h.users.ListTeamMembers(t.OrganizationID, g.ID)
	api.WriteJSON(w, 201, scimGroup(*g, members))
}
func (h *Handler) SCIMPutGroup(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	body, err := readSCIM(r)
	if err != nil {
		scimErr(w, 400, "invalidSyntax", err.Error())
		return
	}
	name := fmt.Sprint(body["displayName"])
	g, err := h.users.UpdateTeam(t.OrganizationID, chi.URLParam(r, "id"), user.UpdateTeamRequest{Name: &name})
	if err != nil {
		scimErr(w, 404, "", "group not found")
		return
	}
	applyMembers(h, t.OrganizationID, g.ID, body)
	members, _ := h.users.ListTeamMembers(t.OrganizationID, g.ID)
	api.WriteJSON(w, 200, scimGroup(*g, members))
}
func (h *Handler) SCIMPatchGroup(w http.ResponseWriter, r *http.Request) { h.SCIMPutGroup(w, r) }
func (h *Handler) SCIMDeleteGroup(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	if err := h.users.DeleteTeam(t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		scimErr(w, 404, "", "group not found")
		return
	}
	w.WriteHeader(204)
}
func applyMembers(h *Handler, orgID, groupID string, body map[string]any) {
	arr, ok := body["members"].([]any)
	if !ok {
		return
	}
	existing, _ := h.users.ListTeamMembers(orgID, groupID)
	for _, m := range existing {
		_ = h.users.RemoveTeamMember(orgID, groupID, m.UserID)
	}
	for _, v := range arr {
		m, _ := v.(map[string]any)
		uid := fmt.Sprint(m["value"])
		if uid != "" {
			_ = h.users.AddTeamMember(orgID, &user.TeamMember{TeamID: groupID, UserID: uid, RoleInTeam: "member"})
		}
	}
}
