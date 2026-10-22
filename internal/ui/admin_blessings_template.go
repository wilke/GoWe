package ui

// Template for the tool-blessings admin page. The nav tab bar is duplicated
// per page like the other admin pages (see admin_templates.go).

func init() {
	templates["admin/blessings"] = adminBlessingsTemplate
}

const adminBlessingsTemplate = `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-4 flex space-x-4 border-b border-gray-200">
        <a href="{{base}}/admin/stats" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Stats</a>
        <a href="{{base}}/admin/health" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Health</a>
        <a href="{{base}}/admin/fleet" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Fleet</a>
        <a href="{{base}}/admin/worker-keys" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Keys</a>
        <a href="{{base}}/admin/tool-blessings" class="pb-2 text-sm font-medium text-indigo-600 border-b-2 border-indigo-500">Blessings</a>
        <a href="{{base}}/admin/outputs" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Outputs</a>
        <a href="{{base}}/admin/labels" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Labels</a>
        <a href="{{base}}/admin/tasks" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Tasks</a>
    </div>
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">Tool Blessings</h1>
        <p class="mt-1 text-sm text-gray-500">An administrator-reviewed allow-list of tools (by content hash) that may receive a user's token, pinned to image sha256 digests. Blessings do not expire; revoking keeps the record. <strong>Blessings are recorded here but not yet enforced anywhere.</strong></p>
    </div>

    {{if .Flash}}<div class="mb-6 bg-green-50 border border-green-200 rounded-lg p-4 text-sm text-green-700">{{.Flash}}</div>{{end}}
    {{if .FlashError}}<div class="mb-6 bg-red-50 border border-red-200 rounded-lg p-4 text-sm text-red-700">{{.FlashError}}</div>{{end}}
    {{if .FormError}}<div class="mb-6 bg-red-50 border border-red-200 rounded-lg p-4 text-sm text-red-700">{{.FormError}}</div>{{end}}

    <!-- Inspect form -->
    <div class="bg-white shadow sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:p-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900 mb-4">Inspect a registered tool</h3>
            <form method="POST" action="{{base}}/admin/tool-blessings/inspect" class="flex flex-wrap items-end gap-4">
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Workflow / tool (name or id)</label>
                    <input type="text" name="workflow" required placeholder="e.g. bvbrc-helper"
                           class="block w-64 rounded-md border-gray-300 shadow-sm text-sm px-3 py-1.5 border">
                </div>
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Tool id (if several)</label>
                    <input type="text" name="tool_id" placeholder="optional"
                           class="block w-48 rounded-md border-gray-300 shadow-sm text-sm px-3 py-1.5 border">
                </div>
                <button type="submit"
                        class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                    Inspect
                </button>
            </form>
        </div>
    </div>

    {{with .Inspect}}
    <div class="bg-white shadow sm:rounded-lg mb-6 border-l-4 border-indigo-500">
        <div class="px-4 py-5 sm:p-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900 mb-2">Review: {{.ToolID}}</h3>
            <dl class="text-sm text-gray-700 space-y-1 mb-4">
                <div><dt class="inline font-medium">Hash:</dt> <dd class="inline font-mono break-all">{{.ToolHash}}</dd></div>
                <div><dt class="inline font-medium">Image ref:</dt> <dd class="inline font-mono">{{if .ImageRef}}{{.ImageRef}}{{else}}-{{end}}</dd></div>
                <div><dt class="inline font-medium">Requests the token:</dt> <dd class="inline">{{.Requests}}</dd></div>
                {{if .Summary.BaseCommand}}<div><dt class="inline font-medium">baseCommand:</dt> <dd class="inline font-mono">{{range $i, $c := .Summary.BaseCommand}}{{if $i}} {{end}}{{$c}}{{end}}</dd></div>{{end}}
                {{range .Summary.Arguments}}<div><dt class="inline font-medium">argument:</dt> <dd class="inline font-mono">{{.}}</dd></div>{{end}}
                {{if .Summary.RequirementClasses}}<div><dt class="inline font-medium">requirements:</dt> <dd class="inline">{{range $i, $c := .Summary.RequirementClasses}}{{if $i}}, {{end}}{{$c}}{{end}}</dd></div>{{end}}
                {{if .Summary.HintClasses}}<div><dt class="inline font-medium">hints:</dt> <dd class="inline">{{range $i, $c := .Summary.HintClasses}}{{if $i}}, {{end}}{{$c}}{{end}}</dd></div>{{end}}
                {{range .Summary.EnvVars}}<div><dt class="inline font-medium">env:</dt> <dd class="inline font-mono">{{.}}</dd></div>{{end}}
            </dl>

            {{range .Summary.Scripts}}
            <div class="mb-4">
                <div class="text-xs font-medium text-gray-500 mb-1">Script: {{.Source}}</div>
                <pre class="bg-gray-900 text-gray-100 text-xs rounded p-3 overflow-x-auto">{{.Content}}</pre>
            </div>
            {{end}}

            {{range .Warnings}}<div class="mb-2 bg-yellow-50 border border-yellow-300 rounded p-3 text-sm text-yellow-800">{{.}}</div>{{end}}

            {{if .Summary.Flags}}
            <div class="mb-4 bg-red-50 border border-red-300 rounded-lg p-4">
                <h4 class="text-sm font-semibold text-red-800 mb-2">Review flags ({{len .Summary.Flags}}) &mdash; confirm each is acceptable</h4>
                <ul class="list-disc pl-5 text-sm text-red-700 space-y-1">{{range .Summary.Flags}}<li>{{.}}</li>{{end}}</ul>
            </div>
            {{else}}
            <p class="mb-4 text-sm text-gray-500">Review flags: none.</p>
            {{end}}

            <p class="mb-4 text-xs text-gray-500">Checklist: read every script; confirm no output (declared stdout/stderr, logs, files) can carry the token &mdash; stderr is where tracebacks and HTTP error bodies land. The token has no audience claim and cannot be revoked upstream.</p>

            {{if .Active}}
            <div class="bg-yellow-50 border border-yellow-300 rounded p-3 text-sm text-yellow-800">An active blessing already exists for this hash ({{.Active.ID}}). Revoke it or add an image below.</div>
            {{else}}
            <form method="POST" action="{{base}}/admin/tool-blessings" class="space-y-3">
                <input type="hidden" name="workflow" value="{{.WorkflowRef}}">
                <input type="hidden" name="tool_id" value="{{.ToolID}}">
                <input type="hidden" name="tool_hash" value="{{.ToolHash}}">
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Image sha256 digest(s), one per line or comma-separated (required; compute with <code>sha256sum image.sif</code> or use <code>gowe admin bless --image-file</code>)</label>
                    <textarea name="image_sha256s" required rows="2" class="block w-full rounded-md border-gray-300 shadow-sm text-sm font-mono px-3 py-1.5 border"></textarea>
                </div>
                <div class="flex flex-wrap gap-4">
                    <div>
                        <label class="block text-xs font-medium text-gray-500 mb-1">Env names (default BVBRC_TOKEN, KB_AUTH_TOKEN)</label>
                        <input type="text" name="env_names" class="block w-64 rounded-md border-gray-300 shadow-sm text-sm px-3 py-1.5 border">
                    </div>
                    <div>
                        <label class="block text-xs font-medium text-gray-500 mb-1">Worker groups (default: any)</label>
                        <input type="text" name="worker_groups" class="block w-48 rounded-md border-gray-300 shadow-sm text-sm px-3 py-1.5 border">
                    </div>
                    <div>
                        <label class="block text-xs font-medium text-gray-500 mb-1">Note</label>
                        <input type="text" name="note" class="block w-64 rounded-md border-gray-300 shadow-sm text-sm px-3 py-1.5 border">
                    </div>
                </div>
                <label class="flex items-center text-sm text-gray-700">
                    <input type="checkbox" name="ack" value="1" class="mr-2"> I have read the scripts and review flags above and approve this tool receiving the user's token.
                </label>
                <button type="submit"
                        class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-red-600 hover:bg-red-700">
                    Bless this tool
                </button>
            </form>
            {{end}}
        </div>
    </div>
    {{end}}

    <!-- Pending -->
    <div class="bg-white shadow overflow-hidden sm:rounded-lg mb-6 table-responsive">
        <div class="px-4 py-3 border-b border-gray-200 text-sm font-medium text-gray-900">Awaiting a blessing ({{len .Pending}})</div>
        <table class="min-w-full divide-y divide-gray-200">
            <thead class="bg-gray-50"><tr>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">Workflow</th>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">Tool</th>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">Image</th>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">Hash</th>
                <th class="px-6 py-3 text-right text-xs font-medium text-gray-500 uppercase">Action</th>
            </tr></thead>
            <tbody class="divide-y divide-gray-200">
                {{range .Pending}}
                <tr>
                    <td class="px-6 py-3 text-sm text-gray-900">{{.WorkflowName}}</td>
                    <td class="px-6 py-3 text-sm text-gray-500">{{.ToolID}}</td>
                    <td class="px-6 py-3 text-sm text-gray-500 font-mono">{{.ImageRef}}</td>
                    <td class="px-6 py-3 text-xs text-gray-400 font-mono">{{.ToolHash}}</td>
                    <td class="px-6 py-3 text-right">
                        <form method="POST" action="{{base}}/admin/tool-blessings/inspect">
                            <input type="hidden" name="workflow" value="{{.WorkflowID}}">
                            <input type="hidden" name="tool_id" value="{{.ToolID}}">
                            <button type="submit" class="text-indigo-600 hover:text-indigo-900 text-sm font-medium">Review</button>
                        </form>
                    </td>
                </tr>
                {{else}}
                <tr><td colspan="5" class="px-6 py-6 text-center text-sm text-gray-500">No token-requesting tools are awaiting a blessing.</td></tr>
                {{end}}
            </tbody>
        </table>
    </div>

    <!-- Blessings -->
    <div class="bg-white shadow overflow-hidden sm:rounded-lg table-responsive">
        <div class="px-4 py-3 border-b border-gray-200 flex justify-between text-sm">
            <span class="font-medium text-gray-900">Blessings</span>
            {{if .ShowAll}}<a href="{{base}}/admin/tool-blessings" class="text-indigo-600">hide revoked</a>
            {{else}}<a href="{{base}}/admin/tool-blessings?all=1" class="text-indigo-600">show revoked</a>{{end}}
        </div>
        <table class="min-w-full divide-y divide-gray-200">
            <thead class="bg-gray-50"><tr>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">Tool</th>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">State</th>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">Image / pinned digests</th>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">Groups</th>
                <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase">Blessed</th>
                <th class="px-6 py-3 text-right text-xs font-medium text-gray-500 uppercase">Actions</th>
            </tr></thead>
            <tbody class="divide-y divide-gray-200">
                {{range .Blessings}}
                <tr id="blessing-{{.ID}}">
                    <td class="px-6 py-3 text-sm text-gray-900">{{.ToolName}}<div class="text-xs text-gray-400 font-mono break-all">{{.ToolHash}}</div>{{if .Note}}<div class="text-xs text-gray-500">{{.Note}}</div>{{end}}</td>
                    <td class="px-6 py-3 text-sm">
                        {{if .RevokedAt}}<span class="inline-flex px-2.5 py-0.5 rounded-full text-xs font-medium bg-gray-200 text-gray-700">revoked</span>
                        <div class="text-xs text-gray-500">{{formatTimePtr .RevokedAt}} by {{.RevokedBy}}: {{.RevokeReason}}</div>
                        {{else}}<span class="inline-flex px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">active</span>{{end}}
                    </td>
                    <td class="px-6 py-3 text-xs text-gray-500 font-mono"><div>{{.ImageRef}}</div>{{range .ImageSHA256s}}<div class="break-all">{{.}}</div>{{end}}</td>
                    <td class="px-6 py-3 text-sm text-gray-500">{{range $i, $g := .WorkerGroups}}{{if $i}}, {{end}}{{$g}}{{else}}any{{end}}</td>
                    <td class="px-6 py-3 text-sm text-gray-500">{{formatTime .BlessedAt}}<div class="text-xs">{{.BlessedBy}}</div></td>
                    <td class="px-6 py-3 text-right text-sm">
                        {{if not .RevokedAt}}
                        <form method="POST" action="{{base}}/admin/tool-blessings/{{.ID}}/images" class="mb-2 flex gap-1 justify-end">
                            <input type="text" name="image_sha256" required placeholder="image sha256" class="w-40 rounded-md border-gray-300 text-xs px-2 py-1 border font-mono">
                            <button type="submit" class="text-indigo-600 hover:text-indigo-900 font-medium">Add image</button>
                        </form>
                        <form method="POST" action="{{base}}/admin/tool-blessings/{{.ID}}/revoke" class="flex gap-1 justify-end"
                              onsubmit="return confirm('Revoke this blessing?');">
                            <input type="text" name="reason" required placeholder="reason" class="w-40 rounded-md border-gray-300 text-xs px-2 py-1 border">
                            <button type="submit" class="text-red-600 hover:text-red-900 font-medium">Revoke</button>
                        </form>
                        {{end}}
                    </td>
                </tr>
                {{else}}
                <tr><td colspan="6" class="px-6 py-8 text-center text-sm text-gray-500">No blessings yet.</td></tr>
                {{end}}
            </tbody>
        </table>
    </div>
</div>
{{end}}`
