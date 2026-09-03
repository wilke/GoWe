package ui

// Template markup for the admin pages added by issue #227 (worker fleet,
// worker keys, output verification/redelivery). Registered into the shared
// `templates` map (templates.go) via init() so renderTemplate can find them
// by name exactly like every other page.
//
// The nav tab bar these pages share with admin/stats, admin/health,
// admin/labels and admin/tasks is duplicated per page rather than factored
// into a component, matching how those four existing admin pages already do
// it (see templates.go).

func init() {
	templates["admin/fleet"] = adminFleetTemplate
	templates["admin/keys"] = adminKeysTemplate
	templates["admin/outputs"] = adminOutputsTemplate
}

const adminFleetTemplate = `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-4 flex space-x-4 border-b border-gray-200">
        <a href="/admin/stats" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Stats</a>
        <a href="/admin/health" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Health</a>
        <a href="/admin/fleet" class="pb-2 text-sm font-medium text-indigo-600 border-b-2 border-indigo-500">Fleet</a>
        <a href="/admin/worker-keys" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Keys</a>
        <a href="/admin/outputs" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Outputs</a>
        <a href="/admin/labels" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Labels</a>
        <a href="/admin/tasks" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Tasks</a>
    </div>
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">Worker Fleet</h1>
        <p class="mt-1 text-sm text-gray-500">
            {{.Total}} worker{{if ne .Total 1}}s{{end}} registered
            &middot; {{.OnlineCount}} online
            &middot; {{.DrainingCount}} draining
            &middot; {{.OfflineCount}} offline
            &middot; {{.GPUCount}} GPU-enabled
            &middot; <a href="/workers" class="text-indigo-600 hover:text-indigo-500">manage individual workers &rarr;</a>
        </p>
    </div>

    {{if .Workers}}
    <div class="bg-white shadow rounded-lg overflow-hidden table-responsive">
        <table class="min-w-full divide-y divide-gray-200">
            <thead class="bg-gray-50">
                <tr>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Status</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Name</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Group</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Runtime</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">GPU</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Current Task</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Version</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Last Seen</th>
                </tr>
            </thead>
            <tbody class="bg-white divide-y divide-gray-200">
                {{range .Workers}}
                <tr id="fleet-worker-{{.ID}}" class="hover:bg-gray-50">
                    <td class="px-6 py-4 whitespace-nowrap">
                        <span class="inline-flex items-center gap-1.5">
                            <span class="w-2.5 h-2.5 rounded-full {{workerStateColor (print .State)}}"></span>
                            <span class="text-sm text-gray-700">{{.State}}</span>
                        </span>
                    </td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm font-medium text-gray-900" title="{{.ID}}">{{.Name}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{.Group}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{.Runtime}}</td>
                    <td class="px-6 py-4 whitespace-nowrap">
                        {{if .GPUEnabled}}
                        <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
                            GPU{{if .GPUDevice}}:{{.GPUDevice}}{{end}}
                        </span>
                        {{else}}
                        <span class="text-gray-400 text-sm">-</span>
                        {{end}}
                    </td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">
                        {{if .CurrentTask}}
                        {{$subID := index $.TaskSubmission .CurrentTask}}
                        {{if $subID}}<a href="/submissions/{{$subID}}" class="text-blue-600 hover:text-blue-800 hover:underline" title="{{.CurrentTask}}">{{truncate .CurrentTask 12}}</a>{{else}}<span class="text-blue-600">{{truncate .CurrentTask 12}}</span>{{end}}
                        {{else}}
                        <span class="text-gray-400">idle</span>
                        {{end}}
                    </td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400 font-mono">{{if .Version}}{{truncate .Version 12}}{{else}}-{{end}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{timeAgo .LastSeen}}</td>
                </tr>
                {{end}}
            </tbody>
        </table>
    </div>
    {{else}}
    <div class="bg-white shadow rounded-lg p-12 text-center">
        <h3 class="mt-2 text-sm font-medium text-gray-900">No workers registered</h3>
        <p class="mt-1 text-sm text-gray-500">Start a worker to see it appear here.</p>
    </div>
    {{end}}
</div>
{{end}}`

const adminKeysTemplate = `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-4 flex space-x-4 border-b border-gray-200">
        <a href="/admin/stats" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Stats</a>
        <a href="/admin/health" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Health</a>
        <a href="/admin/fleet" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Fleet</a>
        <a href="/admin/worker-keys" class="pb-2 text-sm font-medium text-indigo-600 border-b-2 border-indigo-500">Keys</a>
        <a href="/admin/outputs" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Outputs</a>
        <a href="/admin/labels" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Labels</a>
        <a href="/admin/tasks" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Tasks</a>
    </div>
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">Worker Keys</h1>
        <p class="mt-1 text-sm text-gray-500">Per-worker authentication credentials for the X-Worker-Key header</p>
    </div>

    {{if .RawKey}}
    <div class="mb-6 bg-yellow-50 border border-yellow-300 rounded-lg p-4">
        <h3 class="text-sm font-medium text-yellow-800 mb-2">Key issued &mdash; copy it now, it will not be shown again</h3>
        <code class="block bg-white border border-yellow-200 rounded px-3 py-2 text-sm text-gray-900 break-all select-all">{{.RawKey}}</code>
    </div>
    {{end}}

    <!-- Issue new key form -->
    <div class="bg-white shadow sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:p-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900 mb-4">Issue Key</h3>
            <form method="POST" action="/admin/worker-keys" class="flex flex-wrap items-end gap-4">
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Label</label>
                    <input type="text" name="label" placeholder="e.g. gpu-node-3"
                           class="block w-40 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                </div>
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Groups (comma-separated)</label>
                    <input type="text" name="groups" placeholder="default"
                           class="block w-48 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                </div>
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Description</label>
                    <input type="text" name="description" placeholder="Optional"
                           class="block w-48 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                </div>
                <div>
                    <label class="block text-xs font-medium text-gray-500 mb-1">Expires</label>
                    <input type="date" name="expires_at"
                           class="block w-40 rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                </div>
                <button type="submit"
                        class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                    Issue Key
                </button>
            </form>
        </div>
    </div>

    <!-- Existing keys table -->
    <div class="bg-white shadow overflow-hidden sm:rounded-lg table-responsive">
        <table class="min-w-full divide-y divide-gray-200">
            <thead class="bg-gray-50">
                <tr>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Label</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Prefix</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Groups</th>
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
                <tr id="key-{{.ID}}">
                    <td class="px-6 py-4 whitespace-nowrap text-sm font-medium text-gray-900">{{if .Label}}{{.Label}}{{else}}<span class="text-gray-400">(unlabeled)</span>{{end}}</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400 font-mono">{{.KeyPrefix}}&hellip;</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{range $i, $g := .Groups}}{{if $i}}, {{end}}{{$g}}{{end}}</td>
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
                        <button hx-delete="/admin/worker-keys/{{.ID}}"
                                hx-target="#key-{{.ID}}"
                                hx-swap="outerHTML"
                                hx-confirm="Revoke key {{if .Label}}{{.Label}}{{else}}{{.KeyPrefix}}&hellip;{{end}}? This permanently deletes it; any worker using it will be unable to authenticate."
                                class="text-red-600 hover:text-red-900 text-sm font-medium">
                            Revoke
                        </button>
                    </td>
                </tr>
                {{else}}
                <tr>
                    <td colspan="9" class="px-6 py-8 text-center text-sm text-gray-500">
                        No worker keys issued. Issue one above to get started.
                    </td>
                </tr>
                {{end}}
            </tbody>
        </table>
    </div>
</div>
{{end}}`

const adminOutputsTemplate = `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-4 flex space-x-4 border-b border-gray-200">
        <a href="/admin/stats" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Stats</a>
        <a href="/admin/health" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Health</a>
        <a href="/admin/fleet" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Fleet</a>
        <a href="/admin/worker-keys" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Keys</a>
        <a href="/admin/outputs" class="pb-2 text-sm font-medium text-indigo-600 border-b-2 border-indigo-500">Outputs</a>
        <a href="/admin/labels" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Labels</a>
        <a href="/admin/tasks" class="pb-2 text-sm font-medium text-gray-500 hover:text-gray-700">Tasks</a>
    </div>
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">Output Verification &amp; Redelivery</h1>
        <p class="mt-1 text-sm text-gray-500">Compare a submission's delivered workspace outputs against their recorded checksums, and re-upload any that don't match.</p>
    </div>

    {{if .FormError}}
    <div class="mb-6 bg-red-50 border border-red-200 rounded-lg p-4 text-sm text-red-700">{{.FormError}}</div>
    {{end}}
    {{if .CallError}}
    <div class="mb-6 bg-red-50 border border-red-200 rounded-lg p-4 text-sm text-red-700">Call failed: {{.CallError}}</div>
    {{end}}
    {{if .APIError}}
    <div class="mb-6 bg-red-50 border border-red-200 rounded-lg p-4 text-sm text-red-700">API error{{if .HTTPStatus}} (HTTP {{.HTTPStatus}}){{end}}: {{.APIError}}</div>
    {{end}}

    <div class="bg-white shadow sm:rounded-lg mb-8">
        <div class="px-4 py-5 sm:p-6">
            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
                <form method="POST" action="/admin/outputs/verify" class="space-y-3">
                    <div>
                        <label class="block text-xs font-medium text-gray-500 mb-1">Submission ID</label>
                        <input type="text" name="submission_id" required value="{{.SubmissionID}}" placeholder="sub_..."
                               class="block w-full rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                    </div>
                    <button type="submit"
                            class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                        Verify Outputs (read-only)
                    </button>
                </form>
                <form method="POST" action="/admin/outputs/redeliver" class="space-y-3"
                      onsubmit="return confirm('Re-deliver mismatched outputs for this submission? This re-uploads files and updates submission state.');">
                    <div>
                        <label class="block text-xs font-medium text-gray-500 mb-1">Submission ID</label>
                        <input type="text" name="submission_id" required value="{{.SubmissionID}}" placeholder="sub_..."
                               class="block w-full rounded-md border-gray-300 shadow-sm text-sm focus:border-indigo-500 focus:ring-indigo-500 px-3 py-1.5 border">
                    </div>
                    <label class="inline-flex items-center gap-2 text-sm text-gray-600">
                        <input type="checkbox" name="dry_run" class="rounded border-gray-300">
                        Dry run (report only, don't re-upload)
                    </label>
                    <button type="submit"
                            class="inline-flex items-center px-4 py-1.5 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-red-600 hover:bg-red-700">
                        Redeliver
                    </button>
                </form>
            </div>
        </div>
    </div>

    {{with .Report}}
    <div class="bg-white shadow rounded-lg overflow-hidden mb-8">
        <div class="px-4 py-5 border-b border-gray-200 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">
                {{if $.Redeliver}}Redeliver{{else}}Verify{{end}} result for {{.SubmissionID}}{{if $.DryRun}} (dry run){{end}}
            </h3>
            <p class="mt-1 text-sm text-gray-500">State: {{.State}} &middot; Output state: {{.OutputState}}
                {{if .Updated}} &middot; submission updated{{end}}
                {{if .StateRestored}} &middot; state restored to COMPLETED{{end}}
                {{if .ManifestUploaded}} &middot; manifest re-uploaded{{end}}
                {{if .ManifestError}} &middot; manifest error: {{.ManifestError}}{{end}}
            </p>
        </div>
        <div class="p-6 grid grid-cols-2 sm:grid-cols-4 lg:grid-cols-7 gap-4 border-b border-gray-200">
            <div class="text-center p-3 bg-gray-50 rounded-lg">
                <p class="text-xl font-bold text-gray-900">{{.Summary.Total}}</p>
                <p class="text-xs text-gray-500">Total</p>
            </div>
            <div class="text-center p-3 bg-green-50 rounded-lg">
                <p class="text-xl font-bold text-green-600">{{.Summary.OK}}</p>
                <p class="text-xs text-green-800">OK</p>
            </div>
            <div class="text-center p-3 bg-yellow-50 rounded-lg">
                <p class="text-xl font-bold text-yellow-600">{{.Summary.Mismatched}}</p>
                <p class="text-xs text-yellow-800">Mismatched</p>
            </div>
            <div class="text-center p-3 bg-red-50 rounded-lg">
                <p class="text-xl font-bold text-red-600">{{.Summary.Errors}}</p>
                <p class="text-xs text-red-800">Errors</p>
            </div>
            <div class="text-center p-3 bg-blue-50 rounded-lg">
                <p class="text-xl font-bold text-blue-600">{{.Summary.Reuploaded}}</p>
                <p class="text-xs text-blue-800">Reuploaded</p>
            </div>
            <div class="text-center p-3 bg-purple-50 rounded-lg">
                <p class="text-xl font-bold text-purple-600">{{.Summary.WouldReupload}}</p>
                <p class="text-xs text-purple-800">Would Reupload</p>
            </div>
            <div class="text-center p-3 bg-gray-100 rounded-lg">
                <p class="text-xl font-bold text-gray-700">{{.Summary.OriginalMissing}}</p>
                <p class="text-xs text-gray-600">Original Missing</p>
            </div>
        </div>
        <div class="table-responsive">
            <table class="min-w-full divide-y divide-gray-200">
                <thead class="bg-gray-50">
                    <tr>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">OK</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Location</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Action</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Expected</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Actual</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Error</th>
                    </tr>
                </thead>
                <tbody class="bg-white divide-y divide-gray-200">
                    {{range .Files}}
                    <tr class="hover:bg-gray-50">
                        <td class="px-6 py-4 whitespace-nowrap">
                            {{if .OK}}<span class="text-green-600">&#10003;</span>{{else}}<span class="text-red-600">&#10007;</span>{{end}}
                        </td>
                        <td class="px-6 py-4 text-sm text-gray-700 break-all">{{.Location}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{if .Action}}{{.Action}}{{else}}-{{end}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400 font-mono">{{truncate .ExpectedChecksum 12}} ({{.ExpectedSize}})</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-400 font-mono">{{truncate .ActualChecksum 12}} ({{.ActualSize}})</td>
                        <td class="px-6 py-4 text-sm text-red-600">{{.Error}}</td>
                    </tr>
                    {{else}}
                    <tr>
                        <td colspan="6" class="px-6 py-8 text-center text-sm text-gray-500">No output files found for this submission.</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
        </div>
    </div>
    {{end}}
</div>
{{end}}`
