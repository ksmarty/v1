package server

import "testing"

const sampleScopeDocs = "---\ntitle: Scopes\n---\n\nSome intro.\n\n## Available scopes\n\nName | Description\n-----|-----------|\n" +
	"**`(no scope)`** | Grants read-only access to public information{% ifversion ghes %}\n" +
	"**`site_admin`** | Grants site administrators access to [Admin API](/rest/enterprise-admin).{% endif %}\n" +
	"**`repo`** | Grants full access to public{% ifversion ghec or ghes %}, internal,{% endif %} and private repositories. **Note:** also manages projects.\n" +
	"&emsp;`repo:status`| Grants read/write access to commit statuses.\n" +
	"&emsp;`security_events` | Grants: <br/> read and write access to security events.\n" +
	"**`status`** | Grants read/write access to commit statuses in {% ifversion fpt %}public and private{% elsif ghec or ghes %}public, private, and internal{% endif %} repositories.\n" +
	"**`user`** | Grants read/write access to profile info. Note that this scope includes `user:email`.\n" +
	"&emsp;`read:user`| Grants access to read a user's profile data.\n" +
	"**`workflow`** | Grants the ability to add and update {% data variables.product.prodname_actions %} workflow files.\n" +
	"**`read:packages`** | Grants access to download or install packages from {% data variables.product.prodname_registry %}. For more information, see [AUTOTITLE](/packages/installing-a-package).\n" +
	"**`write:packages`** | Grants access to upload a package in {% data variables.product.prodname_registry %}. For more information, see [AUTOTITLE](/packages/publishing-a-package).\n\n" +
	"> [!NOTE]\n> Your app can request scopes.\n\n## Next section\n"

func TestParseGitHubScopes(t *testing.T) {
	list := parseGitHubScopes(sampleScopeDocs)
	got := map[string]string{}
	for _, sc := range list {
		got[sc.Name] = sc.Description
	}
	// The table header, separator, and the pseudo-scope are not scopes.
	if _, ok := got["Name"]; ok {
		t.Errorf("header row leaked into the catalog")
	}
	if _, ok := got["(no scope)"]; ok {
		t.Errorf("(no scope) should be skipped")
	}
	for _, name := range []string{"site_admin", "repo", "repo:status", "security_events", "status", "user", "read:user", "workflow", "read:packages", "write:packages"} {
		if _, ok := got[name]; !ok {
			t.Errorf("missing scope %q", name)
		}
	}
	if want := "Grants the ability to add and update GitHub Actions workflow files."; got["workflow"] != want {
		t.Errorf("workflow description = %q, want %q", got["workflow"], want)
	}
	if want := "Grants full access to public, internal, and private repositories. Note: also manages projects."; got["repo"] != want {
		t.Errorf("repo description = %q, want %q", got["repo"], want)
	}
	// An `{% elsif %}` branch must not be concatenated onto the primary branch.
	if want := "Grants read/write access to commit statuses in public and private repositories."; got["status"] != want {
		t.Errorf("status description = %q, want %q", got["status"], want)
	}
	if want := "Grants access to upload a package in GitHub Packages."; got["write:packages"] != want {
		t.Errorf("write:packages description = %q, want %q", got["write:packages"], want)
	}
	// Scopes from a later section must not leak in.
	if _, ok := got["Next section"]; ok {
		t.Errorf("parsed past the table")
	}
}

func TestMergeSelectedScopes(t *testing.T) {
	list := []GitHubScope{{Name: "repo", Description: "full access"}}
	out := mergeSelectedScopes(list, []string{"repo", "workflow"})
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[1].Name != "workflow" || out[1].Description != "" {
		t.Errorf("missing selected scope appended: %+v", out[1])
	}
}
