package ui

// Templates of the worker group pages (worker_groups.go). The admin tab bar
// is duplicated per page like the other admin pages (see admin_templates.go).

func init() {
	templates["admin/worker_groups"] = adminWorkerGroupsTemplate
	templates["worker_groups/list"] = workerGroupsListTemplate
	templates["worker_groups/detail"] = workerGroupDetailTemplate
	templates["worker_groups/key_created"] = workerGroupKeyCreatedTemplate
}

const adminWorkerGroupsTemplate = `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-4 flex space-x-4 border-b border-gray-200">
        <a href="{{base}}/admin/stats" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Stats</a>
        <a href="{{base}}/admin/health" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Health</a>
        <a href="{{base}}/admin/fleet" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Fleet</a>
        <a href="{{base}}/admin/worker-keys" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Keys</a>
        <a href="{{base}}/admin/worker-groups" class="pb-2 text-sm font-medium text-indigo-600 border-b-2 border-indigo-500">Worker groups</a>
        <a href="{{base}}/admin/tool-blessings" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Blessings</a>
        <a href="{{base}}/admin/outputs" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Outputs</a>
        <a href="{{base}}/admin/labels" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Labels</a>
        <a href="{{base}}/admin/tasks" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Tasks</a>
    </div>
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">Worker Groups</h1>
        <p class="mt-1 text-sm text-gray-500">Which submitters may send tasks to a worker group. A change is stored in the database and in force at once for new submissions, dispatch and retries; tasks already queued or running are not checked again.</p>
    </div>

    {{if .Flash}}<div class="mb-6 bg-green-50 border border-green-200 rounded-lg p-4 text-sm text-green-700">{{.Flash}}</div>{{end}}
    {{if .FormError}}<div id="form-error" class="mb-6 bg-red-50 border border-red-200 rounded-lg p-4 text-sm text-red-700">{{.FormError}}</div>{{end}}
    {{with .Kept}}
    <div id="kept-keys" class="mb-6 bg-yellow-50 border border-yellow-300 rounded-lg p-4 text-sm text-yellow-800">
        <h3 class="font-medium mb-1">{{if .Deleted}}The rule of {{.Group}} was deleted.{{else}}Managers were removed from {{.Group}}.{{end}} These worker keys of the group stay valid:</h3>
        {{if .IDs}}
        <ul class="list-disc pl-5 font-mono">{{range .IDs}}<li>{{.}}</li>{{end}}</ul>
        <p class="mt-2">Revoke them on the <a href="{{base}}/admin/worker-keys" class="text-indigo-600 hover:text-indigo-500">Keys</a> page if they should no longer let workers join the group.</p>
        {{else}}
        <p>None: no worker key is valid for exactly this group.</p>
        {{end}}
    </div>
    {{end}}

    <!-- default_deny -->
    <div id="default-deny" class="bg-white shadow sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:p-6 flex flex-wrap items-center justify-between gap-4">
            <div>
                <h3 class="text-lg leading-6 font-medium text-gray-900">default_deny
                    {{if .DefaultDeny}}<span class="ml-2 inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-red-100 text-red-800">on</span>
                    {{else}}<span class="ml-2 inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-gray-100 text-gray-800">off</span>{{end}}
                </h3>
                <p class="mt-1 text-sm text-gray-500">When on, a worker group without a rule (other than <code>default</code>) may be used by server admins only; when off, such a group is open to everyone.</p>
            </div>
            <form method="POST" action="{{base}}/admin/worker-groups/default-deny"
                  onsubmit="return confirm('{{if .DefaultDeny}}Turn default_deny off? Every group without a rule becomes open to everyone.{{else}}Turn default_deny on? Every group without a rule (except default) becomes usable by server admins only.{{end}}');">
                <input type="hidden" name="default_deny" value="{{if .DefaultDeny}}false{{else}}true{{end}}">
                <button type="submit"
                        class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white {{if .DefaultDeny}}bg-gray-600 hover:bg-gray-700{{else}}bg-red-600 hover:bg-red-700{{end}}">
                    {{if .DefaultDeny}}Turn off{{else}}Turn on{{end}}
                </button>
            </form>
        </div>
    </div>

    <!-- Create or replace a rule -->
    <div id="rule-form" class="bg-white shadow sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:p-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900 mb-4">{{if .Form.Editing}}Edit the rule of {{.Form.Group}}{{else}}Create or replace a rule{{end}}</h3>
            <form method="POST" action="{{base}}/admin/worker-groups" class="space-y-3">
                <div class="flex flex-wrap items-end gap-4">
                    <div>
                        <label class="block text-xs font-medium text-gray-500 mb-1">Group</label>
                        <input type="text" name="group" required value="{{.Form.Group}}" placeholder="e.g. gpu"
                               class="block w-48 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                    </div>
                    <label class="flex items-center text-sm text-gray-700">
                        <input type="checkbox" name="admins" value="1" {{if .Form.Admins}}checked{{end}} class="mr-2">
                        Admins allowed (server admins may use the group without being listed)
                    </label>
                </div>
                <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div>
                        <label class="block text-xs font-medium text-gray-500 mb-1">Users, one per line</label>
                        <textarea name="users" rows="5" class="block w-full rounded-md border-gray-300 shadow-sm text-sm font-mono px-3 py-1.5 border">{{.Form.Users}}</textarea>
                    </div>
                    <div>
                        <label class="block text-xs font-medium text-gray-500 mb-1">Managers, one per line (not allowed on <code>default</code>)</label>
                        <textarea name="managers" rows="5" class="block w-full rounded-md border-gray-300 shadow-sm text-sm font-mono px-3 py-1.5 border">{{.Form.Managers}}</textarea>
                    </div>
                </div>
                <p class="text-xs text-gray-500">Saving replaces the whole rule of the group: BOTH lists are replaced by the boxes as submitted. Users a manager added or removed since this page was loaded are overwritten (reload the page before editing), and an empty managers box removes the managers. Managers may use the group, change its users, issue and revoke its worker keys and remove its offline workers.</p>
                <div class="flex items-center gap-4">
                    <button type="submit"
                            class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                        {{if .Form.Editing}}Replace rule{{else}}Save rule{{end}}
                    </button>
                    {{if .Form.Editing}}<a href="{{base}}/admin/worker-groups" class="text-sm text-gray-500 hover:text-gray-700">Cancel</a>{{end}}
                </div>
            </form>
        </div>
    </div>

    <!-- Rules -->
    <div id="rules" class="bg-white shadow sm:rounded-lg table-responsive">
        <table class="min-w-full divide-y divide-gray-200">
            <thead class="bg-gray-50">
                <tr>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Group</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Users</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Managers</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Admins allowed</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Group keys</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Workers online / total</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Last changed</th>
                    <th class="px-6 py-3 text-right text-xs font-medium text-gray-500 uppercase tracking-wider">Actions</th>
                </tr>
            </thead>
            <tbody class="bg-white divide-y divide-gray-200">
                {{range .Rules}}
                <tr id="rule-{{.Rule.Group}}">
                    <td class="px-6 py-4 whitespace-nowrap text-sm font-medium"><a href="{{base}}/worker-groups/{{.Rule.Group}}" class="text-indigo-600 hover:text-indigo-500">{{.Rule.Group}}</a></td>
                    <td class="px-6 py-4 text-sm text-gray-500">
                        {{if .Rule.Users}}
                        <details>
                            <summary class="cursor-pointer">{{len .Rule.Users}} user{{if ne (len .Rule.Users) 1}}s{{end}}</summary>
                            <ul class="mt-1 font-mono text-xs">{{range .Rule.Users}}<li>{{.}}</li>{{end}}</ul>
                        </details>
                        {{else}}<span class="text-gray-400">none</span>{{end}}
                    </td>
                    <td class="px-6 py-4 text-sm text-gray-500">{{range $i, $m := .Rule.Managers}}{{if $i}}, {{end}}{{$m}}{{else}}<span class="text-gray-400">none</span>{{end}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{if .Rule.Admins}}yes{{else}}no{{end}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{if .KeysKnown}}{{.Keys}}{{else}}?{{end}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{if .WorkersKnown}}{{.WorkersOnline}} / {{.WorkersTotal}}{{else}}?{{end}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{if .Rule.UpdatedBy}}{{.Rule.UpdatedBy}}{{else}}-{{end}}<div class="text-xs">{{formatTime .Rule.UpdatedAt}}</div></td>
                    <td class="px-6 py-4 whitespace-nowrap text-right text-sm">
                        <a href="{{base}}/admin/worker-groups?edit={{.Rule.Group}}" class="text-indigo-600 hover:text-indigo-900 font-medium mr-3">Edit</a>
                        <form method="POST" action="{{base}}/admin/worker-groups/delete" class="inline"
                              onsubmit="return confirm('Delete the rule of worker group {{.Rule.Group}}? The group becomes open to everyone (to server admins only under default_deny); its worker keys stay valid.');">
                            <input type="hidden" name="group" value="{{.Rule.Group}}">
                            <button type="submit" class="text-red-600 hover:text-red-900 font-medium">Delete</button>
                        </form>
                    </td>
                </tr>
                {{else}}
                <tr>
                    <td colspan="8" class="px-6 py-8 text-center text-sm text-gray-500">No worker group has a rule: every group is open{{if .DefaultDeny}}, except that groups other than default are for server admins only (default_deny){{end}}.</td>
                </tr>
                {{end}}
            </tbody>
        </table>
    </div>
</div>
{{end}}`

const workerGroupsListTemplate = `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">Worker Groups</h1>
        <p class="mt-1 text-sm text-gray-500">{{if .IsAdmin}}Every worker group with a rule. Rules, managers and default_deny are changed on the <a href="{{base}}/admin/worker-groups" class="text-indigo-600 hover:text-indigo-500">admin page</a>.{{else}}The worker groups you manage: their users, worker keys and workers.{{end}}</p>
    </div>

    <div class="bg-white shadow sm:rounded-lg table-responsive">
        <table class="min-w-full divide-y divide-gray-200">
            <thead class="bg-gray-50">
                <tr>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Group</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Users</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Managers</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Admins allowed</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Last changed</th>
                </tr>
            </thead>
            <tbody class="bg-white divide-y divide-gray-200">
                {{range .Rules}}
                <tr>
                    <td class="px-6 py-4 whitespace-nowrap text-sm font-medium"><a href="{{base}}/worker-groups/{{.Group}}" class="text-indigo-600 hover:text-indigo-500">{{.Group}}</a></td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{len .Users}}</td>
                    <td class="px-6 py-4 text-sm text-gray-500">{{range $i, $m := .Managers}}{{if $i}}, {{end}}{{$m}}{{else}}<span class="text-gray-400">none</span>{{end}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{if .Admins}}yes{{else}}no{{end}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{if .UpdatedBy}}{{.UpdatedBy}}{{else}}-{{end}} <span class="text-xs">{{formatTime .UpdatedAt}}</span></td>
                </tr>
                {{else}}
                <tr>
                    <td colspan="5" class="px-6 py-8 text-center text-sm text-gray-500">No worker group has a rule.</td>
                </tr>
                {{end}}
            </tbody>
        </table>
    </div>
</div>
{{end}}`

const workerGroupDetailTemplate = `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-6">
        <a href="{{base}}/worker-groups" class="text-sm text-indigo-600 hover:text-indigo-500">&larr; Worker groups</a>
        <h1 class="mt-2 text-2xl font-semibold text-gray-900">Worker group {{.Group}}</h1>
        <p class="mt-1 text-sm text-gray-500">Rule last changed{{if .Rule.UpdatedBy}} by {{.Rule.UpdatedBy}}{{end}} at {{formatTime .Rule.UpdatedAt}}.</p>
    </div>

    {{if .Flash}}<div class="mb-6 bg-green-50 border border-green-200 rounded-lg p-4 text-sm text-green-700">{{.Flash}}</div>{{end}}
    {{if .FormError}}<div id="form-error" class="mb-6 bg-red-50 border border-red-200 rounded-lg p-4 text-sm text-red-700">{{.FormError}}</div>{{end}}

    {{if .RawKey}}
    <div id="raw-key" class="mb-6 bg-yellow-50 border border-yellow-300 rounded-lg p-4">
        <h3 class="text-sm font-medium text-yellow-800 mb-2">Key issued &mdash; copy it now, it will not be shown again</h3>
        <code class="block bg-white border border-yellow-200 rounded px-3 py-2 text-sm text-gray-900 break-all select-all">{{.RawKey}}</code>
        <p class="mt-2 text-xs text-yellow-800">Give it to the worker as a key file (<code>--worker-key-file</code>, mode 0600); the worker then joins {{.Group}} only.</p>
        <p class="mt-2 text-xs text-yellow-800">Do not reload this page: reloading submits the form again and issues another key. <a href="{{base}}/worker-groups/{{.Group}}" class="underline">Open the group page</a> instead.</p>
    </div>
    {{end}}

    <!-- Users -->
    <div id="users" class="bg-white shadow sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:p-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Users</h3>
            <p class="mt-1 mb-4 text-sm text-gray-500">
                Listed users may send tasks to this group.
                Managers: {{range $i, $m := .Rule.Managers}}{{if $i}}, {{end}}{{$m}}{{else}}none{{end}}.
                Admins allowed: {{if .Rule.Admins}}yes{{else}}no{{end}}.
                Managers and admins allowed are changed by server admins{{if .IsAdmin}} on the <a href="{{base}}/admin/worker-groups?edit={{.Group}}" class="text-indigo-600 hover:text-indigo-500">admin page</a>{{end}}.
            </p>
            <form method="POST" action="{{base}}/worker-groups/{{.Group}}/users" class="flex flex-wrap items-end gap-4 mb-4">
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Username</label>
                    <input type="text" name="username" required placeholder="e.g. alice@bvbrc"
                           class="block w-64 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                </div>
                <button type="submit"
                        class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                    Add user
                </button>
            </form>
            <ul class="divide-y divide-gray-200">
                {{range .Rule.Users}}
                <li class="py-2 flex items-center justify-between">
                    <span class="text-sm text-gray-900 font-mono">{{.}}</span>
                    <form method="POST" action="{{base}}/worker-groups/{{$.Group}}/users/remove"
                          onsubmit="return confirm('Remove {{.}} from the users of {{$.Group}}?');">
                        <input type="hidden" name="username" value="{{.}}">
                        <button type="submit" class="text-red-600 hover:text-red-900 text-sm font-medium">Remove</button>
                    </form>
                </li>
                {{else}}
                <li class="py-2 text-sm text-gray-500">No users listed.</li>
                {{end}}
            </ul>
        </div>
    </div>

    <!-- Keys -->
    <div id="keys" class="bg-white shadow sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:p-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Worker keys</h3>
            <p class="mt-1 mb-4 text-sm text-gray-500">Keys valid for exactly this group: a worker started with one joins {{.Group}} and no other group.</p>
            <form method="POST" action="{{base}}/worker-groups/{{.Group}}/keys" class="flex flex-wrap items-end gap-4">
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Label</label>
                    <input type="text" name="label" placeholder="e.g. gpu-node-3"
                           class="block w-40 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                </div>
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Description</label>
                    <input type="text" name="description" placeholder="Optional"
                           class="block w-48 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                </div>
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Expires (optional)</label>
                    <input type="date" name="expires_at"
                           class="block w-40 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                    <p class="mt-1 text-xs text-gray-500">The key expires at 00:00 UTC at the start of the chosen day.</p>
                </div>
                <button type="submit"
                        class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                    Issue Key
                </button>
            </form>
        </div>
        <div class="table-responsive">
            <table class="min-w-full divide-y divide-gray-200">
                <thead class="bg-gray-50">
                    <tr>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Label</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Prefix</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Status</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Created By</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Created</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Expires</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Last Used</th>
                        <th class="px-6 py-3 text-right text-xs font-medium text-gray-500 uppercase tracking-wider">Actions</th>
                    </tr>
                </thead>
                <tbody class="bg-white divide-y divide-gray-200">
                    {{range .Keys}}
                    <tr id="key-{{.ID}}"{{if eq .ID $.NewKeyID}} class="bg-yellow-50"{{end}}>
                        <td class="px-6 py-4 whitespace-nowrap text-sm font-medium text-gray-900" title="{{.Description}}">{{if .Label}}{{.Label}}{{else}}<span class="text-gray-400">(unlabeled)</span>{{end}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400 font-mono">{{.KeyPrefix}}&hellip;</td>
                        <td class="px-6 py-4 whitespace-nowrap">
                            {{if .Disabled}}
                            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-gray-200 text-gray-700">disabled</span>
                            {{else}}
                            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">active</span>
                            {{end}}
                        </td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{.CreatedBy}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{formatTime .CreatedAt}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{formatTimePtr .ExpiresAt}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{formatTimePtr .LastUsedAt}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-right">
                            <form method="POST" action="{{base}}/worker-groups/{{$.Group}}/keys/revoke"
                                  onsubmit="return confirm('Revoke key {{if .Label}}{{.Label}}{{else}}{{.KeyPrefix}}{{end}}? This permanently deletes it; a worker using it can no longer authenticate.');">
                                <input type="hidden" name="id" value="{{.ID}}">
                                <button type="submit" class="text-red-600 hover:text-red-900 text-sm font-medium">Revoke</button>
                            </form>
                        </td>
                    </tr>
                    {{else}}
                    <tr>
                        <td colspan="8" class="px-6 py-8 text-center text-sm text-gray-500">No worker key is valid for exactly this group.</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
        </div>
    </div>

    <!-- Workers -->
    <div id="workers" class="bg-white shadow sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:p-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Workers</h3>
            <p class="mt-1 text-sm text-gray-500">To retire a worker, stop it on its host or revoke its key, wait until it is offline, then remove it once it has sent no heartbeat for {{.RemovalSilence}}; a removed worker that is still running is refused by the server from then on and must be restarted to register again.</p>
        </div>
        <div class="table-responsive">
            <table class="min-w-full divide-y divide-gray-200">
                <thead class="bg-gray-50">
                    <tr>
                        <th class="px-3 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">ID</th>
                        <th class="px-3 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Name</th>
                        <th class="px-3 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">State</th>
                        <th class="px-3 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Hostname</th>
                        <th class="px-3 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Peer address</th>
                        <th class="px-3 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Last Seen</th>
                        <th class="px-3 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Version</th>
                        <th class="px-3 py-3 text-right text-xs font-medium text-gray-500 uppercase tracking-wider"></th>
                    </tr>
                </thead>
                <tbody class="bg-white divide-y divide-gray-200">
                    {{range .Workers}}
                    <tr id="worker-{{.ID}}">
                        <td class="px-3 py-4 whitespace-nowrap text-xs text-gray-400 font-mono">{{.ID}}</td>
                        <td class="px-3 py-4 whitespace-nowrap text-sm font-medium text-gray-900">{{.Name}}</td>
                        <td class="px-3 py-4 whitespace-nowrap">
                            <span class="inline-flex items-center gap-1.5">
                                <span class="w-2.5 h-2.5 rounded-full {{workerStateColor (print .State)}}"></span>
                                <span class="text-sm text-gray-700">{{.State}}</span>
                            </span>
                        </td>
                        <td class="px-3 py-4 whitespace-nowrap text-sm text-gray-500">{{if .Hostname}}{{.Hostname}}{{else}}-{{end}}</td>
                        <td class="px-3 py-4 whitespace-nowrap text-sm text-gray-500 font-mono">{{if .PeerAddr}}{{.PeerAddr}}{{else}}-{{end}}</td>
                        <td class="px-3 py-4 whitespace-nowrap text-sm text-gray-500" title="{{formatTime .LastSeen}}">{{timeAgo .LastSeen}}</td>
                        <td class="px-3 py-4 whitespace-nowrap text-sm text-gray-400 font-mono">{{if .Version}}{{truncate .Version 12}}{{else}}-{{end}}</td>
                        <td class="px-3 py-4 whitespace-nowrap text-right text-sm">
                            {{if .Removable}}
                            <form method="POST" action="{{base}}/worker-groups/{{$.Group}}/workers/remove"
                                  onsubmit="return confirm('Remove worker {{.Name}}? Its running tasks are queued again.');">
                                <input type="hidden" name="id" value="{{.ID}}">
                                <button type="submit" class="text-red-600 hover:text-red-900 font-medium">Remove</button>
                            </form>
                            {{else}}
                            <button type="button" disabled class="text-gray-300 cursor-not-allowed font-medium" title="Only an offline worker that has sent no heartbeat for {{$.RemovalSilence}} can be removed">Remove</button>
                            {{end}}
                        </td>
                    </tr>
                    {{else}}
                    <tr>
                        <td colspan="8" class="px-6 py-8 text-center text-sm text-gray-500">No worker of this group is registered.</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
        </div>
    </div>
</div>
{{end}}`

// workerGroupKeyCreatedTemplate is the answer to a key creation that
// succeeded when the group page could not be read afterwards: the raw key
// must not be lost.
const workerGroupKeyCreatedTemplate = `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-6">
        <a href="{{base}}/worker-groups" class="text-sm text-indigo-600 hover:text-indigo-500">&larr; Worker groups</a>
        <h1 class="mt-2 text-2xl font-semibold text-gray-900">Worker group {{.Group}}</h1>
    </div>

    <div id="form-error" class="mb-6 bg-yellow-50 border border-yellow-300 rounded-lg p-4 text-sm text-yellow-800">
        The key was issued, but the group page could not be loaded: {{.Warning}}
    </div>

    <div id="raw-key" class="mb-6 bg-yellow-50 border border-yellow-300 rounded-lg p-4">
        <h3 class="text-sm font-medium text-yellow-800 mb-2">Key issued &mdash; copy it now, it will not be shown again</h3>
        <code class="block bg-white border border-yellow-200 rounded px-3 py-2 text-sm text-gray-900 break-all select-all">{{.RawKey}}</code>
        <p class="mt-2 text-xs text-yellow-800">Key id: <span class="font-mono">{{.NewKeyID}}</span>. Give it to the worker as a key file (<code>--worker-key-file</code>, mode 0600); the worker then joins {{.Group}} only.</p>
        <p class="mt-2 text-xs text-yellow-800">Do not reload this page: reloading submits the form again and issues another key. <a href="{{base}}/worker-groups/{{.Group}}" class="underline">Open the group page</a> instead.</p>
    </div>
</div>
{{end}}`
