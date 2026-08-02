package domain

import (
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/kernel"
)

func TestSystemPermissionsCoverTheCatalog(t *testing.T) {
	owner, err := SystemPermissions(RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(owner) != len(Permissions()) {
		t.Fatalf("owner holds %d, catalog has %d", len(owner), len(Permissions()))
	}
	admin, err := SystemPermissions(RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range OwnerOnly() {
		if has(admin, permission) {
			t.Fatalf("admin holds %s", permission)
		}
	}
	member, err := SystemPermissions(RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range member {
		if strings.Contains(string(permission), ":manage") {
			t.Fatalf("member holds %s", permission)
		}
	}
	viewer, err := SystemPermissions(RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range viewer {
		if !strings.HasSuffix(string(permission), ":view") {
			t.Fatalf("viewer holds %s", permission)
		}
	}
	if _, err := SystemPermissions("boss"); err == nil {
		t.Fatal("Parse of boss succeeded")
	}
	if _, err := ParsePermission("workspace:delete"); err == nil {
		t.Fatal("workspace:delete is in the catalog")
	}
}

func TestAccessMatrix(t *testing.T) {
	for _, role := range Roles() {
		permissions, err := SystemPermissions(role)
		if err != nil {
			t.Fatal(err)
		}
		subject := joined(permissions)
		for _, visibility := range Visibilities() {
			for _, action := range Actions() {
				if action == ActionCreate {
					continue
				}
				object := Object{
					Type:       TypeTask,
					OwnerID:    subject.UserID,
					Visibility: visibility,
				}
				got := Can(subject, object, action)
				want := has(permissions, ObjectPermission(TypeTask, action))
				if got.Allow != want || (got.Allow && got.Reason != ReasonOwner) {
					t.Errorf("%s %s %s: %+v, want allow %v as owner", role, visibility, action, got, want)
				}
			}
		}
	}
}

func TestPrivateStaysClosed(t *testing.T) {
	adminPerms, err := SystemPermissions(RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	subject := joined(adminPerms)
	owner, err := kernel.NewID[userID]()
	if err != nil {
		t.Fatal(err)
	}
	object := Object{Type: TypeTask, OwnerID: owner, Visibility: VisibilityPrivate}
	if got := Can(subject, object, ActionView); got.Allow {
		t.Fatalf("admin opened a private object: %+v", got)
	}
}

func TestViewerOwnerCannotDelete(t *testing.T) {
	viewer, err := SystemPermissions(RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	subject := joined(viewer)
	object := Object{Type: TypeTask, OwnerID: subject.UserID, Visibility: VisibilityPrivate}
	got := Can(subject, object, ActionDelete)
	if got.Allow || got.Reason != ReasonPermission {
		t.Fatalf("viewer owner delete: %+v", got)
	}
}

func TestShareLevelAndVisibility(t *testing.T) {
	member, err := SystemPermissions(RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	subject := joined(member)
	owner, err := kernel.NewID[userID]()
	if err != nil {
		t.Fatal(err)
	}
	team, err := kernel.NewID[teamID]()
	if err != nil {
		t.Fatal(err)
	}
	subject.Teams = []TeamID{team}
	base := Object{Type: TypeChat, OwnerID: owner, Visibility: VisibilityPrivate}

	view := base
	view.Shares = []Grant{{UserID: subject.UserID, Level: LevelViewer}}
	if got := Can(subject, view, ActionView); !got.Allow || got.Reason != ReasonShare {
		t.Fatalf("viewer share: %+v", got)
	}
	if got := Can(subject, view, ActionEdit); got.Allow {
		t.Fatalf("viewer share allowed edit: %+v", got)
	}

	edit := base
	edit.Shares = []Grant{{TeamID: team, Level: LevelEditor}}
	if got := Can(subject, edit, ActionDelete); !got.Allow || got.Reason != ReasonShare {
		t.Fatalf("editor share: %+v", got)
	}

	roleGrant := base
	roleGrant.Shares = []Grant{{RoleID: subject.RoleID, Level: LevelViewer}}
	if got := Can(subject, roleGrant, ActionView); !got.Allow || got.Reason != ReasonShare {
		t.Fatalf("role share: %+v", got)
	}

	seen := base
	seen.Visibility = VisibilityTeam
	seen.VisibilityTeam = team
	if got := Can(subject, seen, ActionView); !got.Allow || got.Reason != ReasonVisibility {
		t.Fatalf("team visibility: %+v", got)
	}
	if got := Can(subject, seen, ActionEdit); got.Allow {
		t.Fatalf("team visibility allowed edit: %+v", got)
	}

	wide := base
	wide.Visibility = VisibilityEveryone
	if got := Can(subject, wide, ActionView); !got.Allow || got.Reason != ReasonVisibility {
		t.Fatalf("everyone visibility: %+v", got)
	}
}

func TestCustomRoleDropsUnknownNames(t *testing.T) {
	subject := joined([]Permission{ObjectPermission(TypeTask, ActionView), "nope"})
	object := Object{Type: TypeTask, OwnerID: subject.UserID}
	if got := Can(subject, object, ActionView); !got.Allow {
		t.Fatalf("known permission: %+v", got)
	}
	if got := Can(subject, object, ActionEdit); got.Allow {
		t.Fatalf("missing permission: %+v", got)
	}
	chat := object
	chat.Type = TypeChat
	if got := Can(subject, chat, ActionView); got.Allow {
		t.Fatalf("unknown name granted chat: %+v", got)
	}
}

func TestRunPolicyOnlyNarrows(t *testing.T) {
	member, err := SystemPermissions(RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	subject := joined(member)
	subject.Kind = KindRun
	subject.Policy = []Permission{ObjectPermission(TypeTask, ActionView), ObjectPermission(TypeTask, ActionDelete), PermMembersManage}
	object := Object{Type: TypeTask, OwnerID: subject.UserID, Visibility: VisibilityPrivate}
	if got := Can(subject, object, ActionView); !got.Allow {
		t.Fatalf("run view: %+v", got)
	}
	if got := Can(subject, object, ActionDelete); got.Allow || got.Reason != ReasonPolicy {
		t.Fatalf("run delete: %+v", got)
	}
	if got := Can(subject, object, ActionEdit); got.Allow || got.Reason != ReasonPolicy {
		t.Fatalf("run beyond its policy: %+v", got)
	}
	owner, err := SystemPermissions(RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	subject.Permissions = owner
	if got := Permitted(subject, PermMembersManage); got.Allow || got.Reason != ReasonPolicy {
		t.Fatalf("run install permission: %+v", got)
	}

	subject.Policy = nil
	if got := Can(subject, object, ActionView); got.Allow {
		t.Fatalf("run with no policy: %+v", got)
	}
}

func TestAlwaysRefused(t *testing.T) {
	member, err := SystemPermissions(RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	subject := joined(member)
	object := Object{Type: TypeTask, OwnerID: subject.UserID}

	subject.Disabled = true
	if got := Can(subject, object, ActionView); got.Allow || got.Reason != ReasonDisabled {
		t.Fatalf("disabled: %+v", got)
	}
	subject.Disabled = false
	subject.Joined = false
	if got := Can(subject, object, ActionView); got.Allow || got.Reason != ReasonInvited {
		t.Fatalf("invited: %+v", got)
	}
	subject.Joined = true
	agent := subject
	agent.Kind = KindAgent
	agent.Joined = false
	if got := Can(agent, object, ActionView); got.Allow || got.Reason != ReasonAgent {
		t.Fatalf("agent: %+v", got)
	}
	if got := Can(subject, object, "approve"); got.Reason != ReasonUnknown {
		t.Fatalf("unknown action: %+v", got)
	}
	if got := Permitted(subject, "install:explode"); got.Reason != ReasonUnknown {
		t.Fatalf("unknown permission: %+v", got)
	}
}

func TestCreateIsAnInstallDecision(t *testing.T) {
	member, err := SystemPermissions(RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	subject := joined(member)
	if got := Permitted(subject, ObjectPermission(TypeFolder, ActionCreate)); !got.Allow {
		t.Fatalf("member create: %+v", got)
	}
	viewer, err := SystemPermissions(RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	subject.Permissions = viewer
	if got := Permitted(subject, ObjectPermission(TypeFolder, ActionCreate)); got.Allow {
		t.Fatalf("viewer create: %+v", got)
	}
}

func TestListFilterRefusesABadAliasAndABlindRole(t *testing.T) {
	subject := joined(nil)
	if _, _, err := ListFilter("obj;drop", subject, TypeTask, nil); err == nil {
		t.Fatal("bad alias was accepted")
	}
	clause, args, err := ListFilter("obj", subject, TypeTask, nil)
	if err != nil {
		t.Fatal(err)
	}
	if clause != "FALSE" || args != nil {
		t.Fatalf("blind list = %q %v", clause, args)
	}
	viewer, err := SystemPermissions(RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	subject.Permissions = viewer
	clause, _, err = ListFilter("obj", subject, TypeTask, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clause, "visibility = 'team'") || !strings.Contains(clause, "visibility = 'everyone'") || strings.Contains(clause, "identity.") {
		t.Fatalf("filter = %s", clause)
	}
	if !strings.Contains(SharedObjectsSQL(), "grantee_role_id") || !strings.Contains(SharedObjectsSQL(), "team_id") {
		t.Fatal("shared-object query dropped a grantee")
	}
}

func joined(permissions []Permission) Subject {
	user, err := kernel.NewID[userID]()
	if err != nil {
		panic(err)
	}
	role, err := kernel.NewID[roleID]()
	if err != nil {
		panic(err)
	}
	return Subject{
		Kind:        KindUser,
		UserID:      user,
		RoleID:      role,
		Joined:      true,
		Permissions: permissions,
	}
}
