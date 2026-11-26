package ui

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
)

// Worker group pages (#261): /admin/worker-groups for server admins (rules,
// default_deny) and /worker-groups[/{group}] for the managers of a group and
// server admins (users, group keys, workers). Every read and change goes
// through ui.groups (GroupAdmin, implemented by the server with the same
// code as the REST API), which decides who may do what; the handlers only
// parse forms, call it and render its answer or its error.
//
// State-changing requests are plain form POSTs inside the AuthMiddleware
// group (and AdminMiddleware for /admin), exactly like the other admin
// forms: they need the session cookie, which is HttpOnly and
// SameSite=Strict, and SameOriginMiddleware refuses those a browser sent
// from another site. Whether the user is a server admin is derived on every
// request (LiveRoleMiddleware, AdminMiddleware). After a change the
// browser is redirected (303) to the page with a fixed ?done= code, except
// when the answer carries something to show once: a new key's raw value, or
// the group keys that stay valid after managers or a rule were removed; that
// answer is the page itself (never cached, never in a URL).

// groupDoneMessages are the confirmations chosen by the ?done= code of a
// redirect after a change; text from the URL is never rendered.
var groupDoneMessages = map[string]string{
	"rule-saved":       "Rule saved; it is in force now.",
	"rule-deleted":     "Rule deleted; the change is in force now.",
	"default-deny-on":  "default_deny is on; it is in force now.",
	"default-deny-off": "default_deny is off; it is in force now.",
	"user-added":       "User added; the change is in force now.",
	"user-removed":     "User removed; the change is in force now.",
	"key-revoked":      "Key revoked.",
	"worker-removed":   "Worker removed.",
}

// groupDoneMessage is the confirmation for r's ?done= code ("" for none or
// an unknown code).
func groupDoneMessage(r *http.Request) string {
	return groupDoneMessages[r.URL.Query().Get("done")]
}

// redirectDone redirects (303) to path with the ?done= code.
func (ui *UI) redirectDone(w http.ResponseWriter, r *http.Request, path, code string) {
	http.Redirect(w, r, ui.href(path+"?done="+url.QueryEscape(code)), http.StatusSeeOther)
}

// groupPagePath is the path of the page of group.
func groupPagePath(group string) string {
	return "/worker-groups/" + url.PathEscape(group)
}

// requireGroupAdmin answers 503 when the UI was built without a GroupAdmin.
func (ui *UI) requireGroupAdmin(w http.ResponseWriter, r *http.Request) bool {
	if ui.groups != nil {
		return true
	}
	ui.renderGroupError(w, r, &GroupError{Kind: GroupErrUnavailable, Message: "worker group management is not available on this server"})
	return false
}

// renderGroupError renders the error page for an operation error that keeps
// a page from being shown, with the error's status.
func (ui *UI) renderGroupError(w http.ResponseWriter, r *http.Request, err error) {
	ge := AsGroupError(err)
	ui.renderStatus(w, ge.HTTPStatus(), "error", map[string]any{
		"Title":   "Error - GoWe",
		"Session": SessionFromContext(r.Context()),
		"Message": ge.Message,
	})
}

// requestGroupActor is the session user of r as a GroupActor
// (actorFromContext).
func requestGroupActor(r *http.Request) GroupActor {
	return actorFromContext(r.Context())
}

// --- Admin page: /admin/worker-groups ---

// adminGroupRow is one rule on the admin page with its counts.
type adminGroupRow struct {
	Rule          *model.WorkerGroupPolicy
	Keys          int
	KeysKnown     bool
	WorkersOnline int
	WorkersTotal  int
	WorkersKnown  bool
}

// ruleForm is the create/replace form of the admin page.
type ruleForm struct {
	Group    string
	Users    string
	Managers string
	Admins   bool
	// Editing is set when the form was pre-filled from a stored rule.
	Editing bool
}

// ruleFormOf is the form pre-filled with rule.
func ruleFormOf(rule *model.WorkerGroupPolicy) *ruleForm {
	return &ruleForm{
		Group:    rule.Group,
		Users:    strings.Join(rule.Users, "\n"),
		Managers: strings.Join(rule.Managers, "\n"),
		Admins:   rule.Admins,
		Editing:  true,
	}
}

// keptKeysView names the worker keys of a group that stay valid after its
// managers or its rule were removed.
type keptKeysView struct {
	Group   string
	IDs     []string
	Deleted bool
}

// adminGroupsPage is what a render of the admin page shows besides the
// rules.
type adminGroupsPage struct {
	status  int
	form    *ruleForm // nil: empty, or pre-filled from ?edit=
	formErr string
	flash   string
	kept    *keptKeysView
}

// renderAdminWorkerGroups renders the admin page. It refuses (403) an actor
// who is not a server admin now, and a listing without default_deny (which
// ListRules gives server admins only).
func (ui *UI) renderAdminWorkerGroups(w http.ResponseWriter, r *http.Request, p adminGroupsPage) {
	ctx := r.Context()
	sess := SessionFromContext(ctx)
	actor := actorFromContext(ctx)
	errNotAdmin := &GroupError{Kind: GroupErrForbidden, Message: "admin access required"}
	if !actor.IsAdmin {
		ui.renderGroupError(w, r, errNotAdmin)
		return
	}
	list, err := ui.groups.ListRules(ctx, actor)
	if err != nil {
		ui.renderGroupError(w, r, err)
		return
	}
	if !list.DefaultDenyKnown {
		ui.renderGroupError(w, r, errNotAdmin)
		return
	}
	rows := make([]adminGroupRow, 0, len(list.Rules))
	for _, rule := range list.Rules {
		row := adminGroupRow{Rule: rule}
		// The counts are best effort: a rule whose keys or workers cannot be
		// listed shows "?".
		if keys, err := ui.groups.ListGroupKeys(ctx, actor, rule.Group); err == nil {
			row.Keys, row.KeysKnown = len(keys), true
		}
		if workers, err := ui.groups.ListGroupWorkers(ctx, actor, rule.Group); err == nil {
			row.WorkersTotal, row.WorkersKnown = len(workers), true
			for _, wk := range workers {
				if wk.State == model.WorkerStateOnline {
					row.WorkersOnline++
				}
			}
		}
		rows = append(rows, row)
	}
	form := p.form
	if form == nil {
		form = &ruleForm{}
		if edit := r.URL.Query().Get("edit"); edit != "" {
			for _, rule := range list.Rules {
				if rule.Group == edit {
					form = ruleFormOf(rule)
				}
			}
		}
	}
	status := p.status
	if status == 0 {
		status = http.StatusOK
	}
	ui.renderStatus(w, status, "admin/worker_groups", map[string]any{
		"Title":       "Worker Groups - GoWe",
		"Session":     sess,
		"DefaultDeny": list.DefaultDeny,
		"Rules":       rows,
		"Form":        form,
		"FormError":   p.formErr,
		"Flash":       p.flash,
		"Kept":        p.kept,
	})
}

// renderAdminGroupsError renders the admin page with the error of an
// operation (its status and message) and the submitted form, if any.
func (ui *UI) renderAdminGroupsError(w http.ResponseWriter, r *http.Request, err error, form *ruleForm) {
	ge := AsGroupError(err)
	ui.renderAdminWorkerGroups(w, r, adminGroupsPage{status: ge.HTTPStatus(), form: form, formErr: ge.Message})
}

// HandleAdminWorkerGroups renders the admin page: rules, default_deny and
// the create/replace form (?edit={group} pre-fills it).
// GET /admin/worker-groups
func (ui *UI) HandleAdminWorkerGroups(w http.ResponseWriter, r *http.Request) {
	if !ui.requireGroupAdmin(w, r) {
		return
	}
	ui.renderAdminWorkerGroups(w, r, adminGroupsPage{flash: groupDoneMessage(r)})
}

// HandleAdminWorkerGroupPut creates or replaces a rule from the form: group,
// users and managers one per line, admins checkbox. The managers box
// replaces the stored managers (the form is pre-filled with them when
// editing), so an empty box removes them.
// POST /admin/worker-groups
func (ui *UI) HandleAdminWorkerGroupPut(w http.ResponseWriter, r *http.Request) {
	if !ui.requireGroupAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	form := &ruleForm{
		Group:    strings.TrimSpace(r.FormValue("group")),
		Users:    r.FormValue("users"),
		Managers: r.FormValue("managers"),
		Admins:   r.FormValue("admins") != "",
	}
	res, err := ui.groups.PutRule(r.Context(), requestGroupActor(r), form.Group, PutRuleInput{
		Users:    splitList(form.Users, "\n"),
		Managers: splitList(form.Managers, "\n"),
		Admins:   form.Admins,
	})
	if err != nil {
		ui.renderAdminGroupsError(w, r, err, form)
		return
	}
	if res.GroupKeyIDs != nil {
		// Managers were taken away: show the keys that stay valid, once.
		w.Header().Set("Cache-Control", "no-store")
		ui.renderAdminWorkerGroups(w, r, adminGroupsPage{
			flash: groupDoneMessages["rule-saved"],
			kept:  &keptKeysView{Group: res.Rule.Group, IDs: *res.GroupKeyIDs},
		})
		return
	}
	ui.redirectDone(w, r, "/admin/worker-groups", "rule-saved")
}

// HandleAdminWorkerGroupDelete deletes the rule named by the form field
// group. (A stored rule the policy refuses makes the whole page an error
// page; such a rule is removed with "gowe admin worker-groups delete".)
// POST /admin/worker-groups/delete
func (ui *UI) HandleAdminWorkerGroupDelete(w http.ResponseWriter, r *http.Request) {
	if !ui.requireGroupAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	res, err := ui.groups.DeleteRule(r.Context(), requestGroupActor(r), r.FormValue("group"))
	if err != nil {
		ui.renderAdminGroupsError(w, r, err, nil)
		return
	}
	if res.GroupKeyIDs != nil {
		w.Header().Set("Cache-Control", "no-store")
		ui.renderAdminWorkerGroups(w, r, adminGroupsPage{
			flash: groupDoneMessages["rule-deleted"],
			kept:  &keptKeysView{Group: res.Group, IDs: *res.GroupKeyIDs, Deleted: true},
		})
		return
	}
	ui.redirectDone(w, r, "/admin/worker-groups", "rule-deleted")
}

// HandleAdminWorkerGroupsDefaultDeny sets default_deny from the form field
// default_deny ("true" or "false").
// POST /admin/worker-groups/default-deny
func (ui *UI) HandleAdminWorkerGroupsDefaultDeny(w http.ResponseWriter, r *http.Request) {
	if !ui.requireGroupAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	var deny bool
	switch r.FormValue("default_deny") {
	case "true":
		deny = true
	case "false":
	default:
		ui.renderAdminGroupsError(w, r, &GroupError{Kind: GroupErrInvalid, Message: "default_deny must be true or false"}, nil)
		return
	}
	if err := ui.groups.SetDefaultDeny(r.Context(), requestGroupActor(r), deny); err != nil {
		ui.renderAdminGroupsError(w, r, err, nil)
		return
	}
	code := "default-deny-off"
	if deny {
		code = "default-deny-on"
	}
	ui.redirectDone(w, r, "/admin/worker-groups", code)
}

// --- Group pages: /worker-groups, /worker-groups/{group} ---

// HandleWorkerGroups lists the groups the user manages (a server admin: all
// rules). A user who manages none gets 403.
// GET /worker-groups
func (ui *UI) HandleWorkerGroups(w http.ResponseWriter, r *http.Request) {
	if !ui.requireGroupAdmin(w, r) {
		return
	}
	sess := SessionFromContext(r.Context())
	actor := actorFromContext(r.Context())
	list, err := ui.groups.ListRules(r.Context(), actor)
	if err != nil {
		ui.renderGroupError(w, r, err)
		return
	}
	if !actor.IsAdmin && len(list.Rules) == 0 {
		ui.renderGroupError(w, r, &GroupError{Kind: GroupErrForbidden, Message: "you do not manage any worker group"})
		return
	}
	ui.render(w, "worker_groups/list", map[string]any{
		"Title":   "Worker Groups - GoWe",
		"Session": sess,
		"Rules":   list.Rules,
		"IsAdmin": actor.IsAdmin,
	})
}

// groupPage is what a render of a group page shows besides the group.
type groupPage struct {
	status   int
	formErr  string
	flash    string
	rawKey   string
	newKeyID string
}

// renderWorkerGroup renders the page of group: its users, keys and workers.
// When the group cannot be shown (the user may not manage it, it has no
// rule, ...) the error page is rendered instead, with that error's status;
// but when p carries a new key's raw value, a minimal page with the key
// (renderKeyCreatedFallback), so the key is never lost.
func (ui *UI) renderWorkerGroup(w http.ResponseWriter, r *http.Request, group string, p groupPage) {
	ctx := r.Context()
	sess := SessionFromContext(ctx)
	actor := actorFromContext(ctx)
	fail := func(err error) {
		if p.rawKey != "" {
			ui.renderKeyCreatedFallback(w, r, group, p, err)
			return
		}
		ui.renderGroupError(w, r, err)
	}
	rule, err := ui.groups.GetRule(ctx, actor, group)
	if err != nil {
		fail(err)
		return
	}
	keys, err := ui.groups.ListGroupKeys(ctx, actor, group)
	if err != nil {
		fail(err)
		return
	}
	workers, err := ui.groups.ListGroupWorkers(ctx, actor, group)
	if err != nil {
		fail(err)
		return
	}
	status := p.status
	if status == 0 {
		status = http.StatusOK
	}
	if p.rawKey != "" {
		// The raw key exists in this answer only: never stored by a cache.
		w.Header().Set("Cache-Control", "no-store")
	}
	ui.renderStatus(w, status, "worker_groups/detail", map[string]any{
		"Title":          "Worker group " + rule.Group + " - GoWe",
		"Session":        sess,
		"Group":          rule.Group,
		"Rule":           rule,
		"Keys":           keys,
		"Workers":        workers,
		"IsAdmin":        actor.IsAdmin,
		"RemovalSilence": ui.removalSilence().String(),
		"Flash":          p.flash,
		"FormError":      p.formErr,
		"RawKey":         p.rawKey,
		"NewKeyID":       p.newKeyID,
	})
}

// renderKeyCreatedFallback answers a key creation that succeeded when the
// page could not be read afterwards: a minimal page (200, never cached) with
// the raw key and the read's error as a warning.
func (ui *UI) renderKeyCreatedFallback(w http.ResponseWriter, r *http.Request, group string, p groupPage, err error) {
	ge := AsGroupError(err)
	ui.logger.Warn("worker group key issued but the group page could not be read; showing the key on a minimal page",
		"group", group, "key_id", p.newKeyID, "error", ge.Message)
	w.Header().Set("Cache-Control", "no-store")
	ui.renderStatus(w, http.StatusOK, "worker_groups/key_created", map[string]any{
		"Title":    "Worker group " + group + " - GoWe",
		"Session":  SessionFromContext(r.Context()),
		"Group":    group,
		"RawKey":   p.rawKey,
		"NewKeyID": p.newKeyID,
		"Warning":  ge.Message,
	})
}

// renderWorkerGroupError renders the page of group with the error of an
// operation (its status and message).
func (ui *UI) renderWorkerGroupError(w http.ResponseWriter, r *http.Request, group string, err error) {
	ge := AsGroupError(err)
	ui.renderWorkerGroup(w, r, group, groupPage{status: ge.HTTPStatus(), formErr: ge.Message})
}

// parseGroupForm parses the form of a POST on a group page and returns the
// group from the path; false when the form is invalid (answered).
func (ui *UI) parseGroupForm(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !ui.requireGroupAdmin(w, r) {
		return "", false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return "", false
	}
	return ui.pathParam(r, "group"), true
}

// HandleWorkerGroup renders the page of one group.
// GET /worker-groups/{group}
func (ui *UI) HandleWorkerGroup(w http.ResponseWriter, r *http.Request) {
	if !ui.requireGroupAdmin(w, r) {
		return
	}
	ui.renderWorkerGroup(w, r, ui.pathParam(r, "group"), groupPage{flash: groupDoneMessage(r)})
}

// HandleWorkerGroupUserAdd adds the form's username to the users of the
// group.
// POST /worker-groups/{group}/users
func (ui *UI) HandleWorkerGroupUserAdd(w http.ResponseWriter, r *http.Request) {
	group, ok := ui.parseGroupForm(w, r)
	if !ok {
		return
	}
	if _, err := ui.groups.AddGroupUser(r.Context(), requestGroupActor(r), group, strings.TrimSpace(r.FormValue("username"))); err != nil {
		ui.renderWorkerGroupError(w, r, group, err)
		return
	}
	ui.redirectDone(w, r, groupPagePath(group), "user-added")
}

// HandleWorkerGroupUserRemove removes the form's username from the users of
// the group (a field, not the path: usernames may hold '/').
// POST /worker-groups/{group}/users/remove
func (ui *UI) HandleWorkerGroupUserRemove(w http.ResponseWriter, r *http.Request) {
	group, ok := ui.parseGroupForm(w, r)
	if !ok {
		return
	}
	if _, err := ui.groups.RemoveGroupUser(r.Context(), requestGroupActor(r), group, r.FormValue("username")); err != nil {
		ui.renderWorkerGroupError(w, r, group, err)
		return
	}
	ui.redirectDone(w, r, groupPagePath(group), "user-removed")
}

// HandleWorkerGroupKeyCreate issues a key valid for exactly the group. Like
// the admin worker-key page, success renders the page directly with the raw
// key (never cached), instead of redirecting, which would drop it or put it
// in a URL. Reloading that answer submits the form again and issues another
// key (the page says so). When the page cannot be read after the key was
// issued, the key is shown on a minimal page with the error as a warning.
// POST /worker-groups/{group}/keys
func (ui *UI) HandleWorkerGroupKeyCreate(w http.ResponseWriter, r *http.Request) {
	group, ok := ui.parseGroupForm(w, r)
	if !ok {
		return
	}
	in := GroupKeyInput{
		Label:       strings.TrimSpace(r.FormValue("label")),
		Description: strings.TrimSpace(r.FormValue("description")),
	}
	if raw := strings.TrimSpace(r.FormValue("expires_at")); raw != "" {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			ui.renderWorkerGroupError(w, r, group, &GroupError{Kind: GroupErrInvalid, Message: "invalid expiry date (want YYYY-MM-DD)"})
			return
		}
		in.ExpiresAt = &t
	}
	created, err := ui.groups.CreateGroupKey(r.Context(), requestGroupActor(r), group, in)
	if err != nil {
		ui.renderWorkerGroupError(w, r, group, err)
		return
	}
	ui.renderWorkerGroup(w, r, group, groupPage{rawKey: created.Raw, newKeyID: created.Key.ID})
}

// HandleWorkerGroupKeyRevoke revokes the key named by the form field id.
// POST /worker-groups/{group}/keys/revoke
func (ui *UI) HandleWorkerGroupKeyRevoke(w http.ResponseWriter, r *http.Request) {
	group, ok := ui.parseGroupForm(w, r)
	if !ok {
		return
	}
	if err := ui.groups.RevokeGroupKey(r.Context(), requestGroupActor(r), group, r.FormValue("id")); err != nil {
		ui.renderWorkerGroupError(w, r, group, err)
		return
	}
	ui.redirectDone(w, r, groupPagePath(group), "key-revoked")
}

// HandleWorkerGroupWorkerRemove removes the worker named by the form field
// id; the server refuses a worker that is not removable yet, with a message
// saying why.
// POST /worker-groups/{group}/workers/remove
func (ui *UI) HandleWorkerGroupWorkerRemove(w http.ResponseWriter, r *http.Request) {
	group, ok := ui.parseGroupForm(w, r)
	if !ok {
		return
	}
	if err := ui.groups.RemoveGroupWorker(r.Context(), requestGroupActor(r), group, r.FormValue("id")); err != nil {
		ui.renderWorkerGroupError(w, r, group, err)
		return
	}
	ui.redirectDone(w, r, groupPagePath(group), "worker-removed")
}
