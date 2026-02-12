package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Template functions available in all templates.
var templateFuncs = template.FuncMap{
	"formatTime": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02 15:04:05")
	},
	"formatTimePtr": func(t *time.Time) string {
		if t == nil || t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02 15:04:05")
	},
	"formatDate": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02")
	},
	"formatDuration": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return time.Since(t).Round(time.Second).String()
	},
	"stateColor": func(state string) string {
		switch strings.ToUpper(state) {
		case "PENDING", "SCHEDULED":
			return "yellow"
		case "RUNNING", "QUEUED":
			return "blue"
		case "SUCCESS", "COMPLETED":
			return "green"
		case "FAILED":
			return "red"
		case "CANCELLED", "SKIPPED":
			return "gray"
		default:
			return "gray"
		}
	},
	"stateDotColor": func(state string) string {
		// Returns Tailwind bg color classes for state dots
		switch strings.ToUpper(state) {
		case "PENDING":
			return "bg-gray-300"
		case "SCHEDULED":
			return "bg-yellow-400"
		case "QUEUED":
			return "bg-yellow-500"
		case "RUNNING":
			return "bg-blue-500 animate-pulse"
		case "SUCCESS", "COMPLETED":
			return "bg-green-500"
		case "FAILED":
			return "bg-red-500"
		case "RETRYING":
			return "bg-orange-500 animate-pulse"
		case "SKIPPED", "CANCELLED":
			return "bg-gray-400"
		default:
			return "bg-gray-300"
		}
	},
	"statePillGradient": func(state string) string {
		// Returns CSS gradient for stage pills (AWE-style)
		switch strings.ToUpper(state) {
		case "PENDING":
			return "background: linear-gradient(to bottom, #9CA3AF, #6B7280);"
		case "SCHEDULED", "QUEUED":
			return "background: linear-gradient(to bottom, #FBB450, #F89406);"
		case "RUNNING":
			return "background: linear-gradient(to bottom, #0088CC, #0044CC);"
		case "SUCCESS", "COMPLETED":
			return "background: linear-gradient(to bottom, #62C462, #51A351);"
		case "FAILED":
			return "background: linear-gradient(to bottom, #EE5F5B, #BD362F);"
		case "RETRYING":
			return "background: linear-gradient(to bottom, #F97316, #EA580C);"
		case "SKIPPED", "CANCELLED":
			return "background: linear-gradient(to bottom, #D1D5DB, #9CA3AF);"
		default:
			return "background: linear-gradient(to bottom, #9CA3AF, #6B7280);"
		}
	},
	"add": func(a, b int) int {
		return a + b
	},
	"sub": func(a, b int) int {
		return a - b
	},
	"mul": func(a, b int) int {
		return a * b
	},
	"div": func(a, b int) int {
		if b == 0 {
			return 0
		}
		return a / b
	},
	"percent": func(a, b int) int {
		if b == 0 {
			return 0
		}
		return (a * 100) / b
	},
	"toJSON": func(v any) template.JS {
		b, err := json.Marshal(v)
		if err != nil {
			return template.JS("[]")
		}
		return template.JS(b)
	},
	"truncate": func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return s[:n] + "..."
	},
	"json": func(v any) string {
		// Simple JSON output for debugging.
		return fmt.Sprintf("%+v", v)
	},
	"seq": func(n int) []int {
		// Generate a sequence 0..n-1 for iteration
		result := make([]int, n)
		for i := range result {
			result[i] = i
		}
		return result
	},
	"isFileType": func(t string) bool {
		// Check if CWL type is a File type (including optional File?)
		t = strings.TrimSuffix(t, "?")
		return t == "File" || strings.HasPrefix(t, "File[")
	},
	"isArrayType": func(t string) bool {
		// Check if CWL type is an array type
		t = strings.TrimSuffix(t, "?")
		return strings.HasSuffix(t, "[]") || strings.HasPrefix(t, "File[]")
	},
	"urlquery": func(s string) string {
		return template.URLQueryEscaper(s)
	},
	"isTool": func(class string) bool {
		return class == "CommandLineTool" || class == "ExpressionTool"
	},
	"classBadge": func(class string) string {
		if class == "CommandLineTool" || class == "ExpressionTool" {
			return "Tool"
		}
		return "Workflow"
	},
	"classBadgeColor": func(class string) string {
		if class == "CommandLineTool" || class == "ExpressionTool" {
			return "bg-purple-100 text-purple-800"
		}
		return "bg-indigo-100 text-indigo-800"
	},
}

// renderTemplate renders a template with the given data.
func renderTemplate(w io.Writer, name string, data map[string]any) error {
	// Get the template content.
	content, ok := templates[name]
	if !ok {
		return fmt.Errorf("template not found: %s", name)
	}

	// Get the layout template.
	layout, ok := templates["layout"]
	if !ok {
		return fmt.Errorf("layout template not found")
	}

	// Parse templates.
	tmpl, err := template.New("layout").Funcs(templateFuncs).Parse(layout)
	if err != nil {
		return fmt.Errorf("parse layout: %w", err)
	}

	_, err = tmpl.New("content").Parse(content)
	if err != nil {
		return fmt.Errorf("parse content: %w", err)
	}

	// Add shared components.
	for compName, compContent := range templates {
		if strings.HasPrefix(compName, "components/") {
			_, err = tmpl.New(filepath.Base(compName)).Parse(compContent)
			if err != nil {
				return fmt.Errorf("parse component %s: %w", compName, err)
			}
		}
	}

	return tmpl.Execute(w, data)
}

// templates holds all template content. In a production app, these would be
// loaded from files or generated by templ.
var templates = map[string]string{
	"layout": `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://unpkg.com/vue@3/dist/vue.global.prod.js"></script>
    <script src="/static/js/dag-editor.js"></script>
    <script src="/static/js/app.js"></script>
    <link rel="stylesheet" href="/static/css/app.css">
    <style>
        [x-cloak] { display: none !important; }
        .htmx-indicator { display: none; }
        .htmx-request .htmx-indicator { display: inline-block; }
        .htmx-request.htmx-indicator { display: inline-block; }
    </style>
</head>
<body class="bg-gray-50 min-h-screen">
    {{if .Session}}
    <nav class="bg-white shadow-sm border-b">
        <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div class="flex justify-between h-16">
                <div class="flex">
                    <a href="/" class="flex items-center px-2 py-2 text-xl font-bold text-indigo-600">
                        GoWe
                    </a>
                    <div class="hidden sm:ml-6 sm:flex sm:space-x-8">
                        <a href="/" class="border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700 inline-flex items-center px-1 pt-1 border-b-2 text-sm font-medium">
                            Dashboard
                        </a>
                        <a href="/workflows" class="border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700 inline-flex items-center px-1 pt-1 border-b-2 text-sm font-medium">
                            Workflows
                        </a>
                        <a href="/submissions" class="border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700 inline-flex items-center px-1 pt-1 border-b-2 text-sm font-medium">
                            Submissions
                        </a>
                        <a href="/workspace" class="border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700 inline-flex items-center px-1 pt-1 border-b-2 text-sm font-medium">
                            Workspace
                        </a>
                        {{if .Session.IsAdmin}}
                        <a href="/admin" class="border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700 inline-flex items-center px-1 pt-1 border-b-2 text-sm font-medium">
                            Admin
                        </a>
                        {{end}}
                    </div>
                </div>
                <div class="flex items-center">
                    <span class="text-sm text-gray-500 mr-4">{{.Session.Username}}</span>
                    <a href="/logout" class="text-sm text-gray-500 hover:text-gray-700">Logout</a>
                </div>
            </div>
        </div>
    </nav>
    {{end}}

    <main class="max-w-7xl mx-auto py-6 sm:px-6 lg:px-8">
        {{template "content" .}}
    </main>
</body>
</html>`,

	"login": `{{define "content"}}
<div class="min-h-screen flex items-center justify-center bg-gray-50 py-12 px-4 sm:px-6 lg:px-8">
    <div class="max-w-md w-full space-y-8">
        <div>
            <h2 class="mt-6 text-center text-3xl font-extrabold text-gray-900">
                GoWe Workflow Engine
            </h2>
            <p class="mt-2 text-center text-sm text-gray-600">
                Sign in with your BV-BRC account
            </p>
        </div>
        {{if .Error}}
        <div class="rounded-md bg-red-50 p-4">
            <div class="text-sm text-red-700">{{.Error}}</div>
        </div>
        {{end}}
        <form class="mt-8 space-y-6" action="/login" method="POST">
            <div class="rounded-md shadow-sm -space-y-px">
                <div>
                    <label for="username" class="sr-only">Username</label>
                    <input id="username" name="username" type="text" required
                           class="appearance-none rounded-none relative block w-full px-3 py-2 border border-gray-300 placeholder-gray-500 text-gray-900 rounded-t-md focus:outline-none focus:ring-indigo-500 focus:border-indigo-500 focus:z-10 sm:text-sm"
                           placeholder="BV-BRC Username">
                </div>
                <div>
                    <label for="password" class="sr-only">Password</label>
                    <input id="password" name="password" type="password" required
                           class="appearance-none rounded-none relative block w-full px-3 py-2 border border-gray-300 placeholder-gray-500 text-gray-900 rounded-b-md focus:outline-none focus:ring-indigo-500 focus:border-indigo-500 focus:z-10 sm:text-sm"
                           placeholder="Password">
                </div>
            </div>
            <div>
                <button type="submit"
                        class="group relative w-full flex justify-center py-2 px-4 border border-transparent text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-indigo-500">
                    Sign in
                </button>
            </div>
        </form>
    </div>
</div>
{{end}}`,

	"dashboard": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">Dashboard</h1>
        <p class="mt-1 text-sm text-gray-500">Welcome back, {{.Session.Username}}</p>
    </div>

    <!-- Stats -->
    <div class="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-4 mb-8">
        <div class="bg-white overflow-hidden shadow rounded-lg">
            <div class="p-5">
                <div class="flex items-center">
                    <div class="flex-shrink-0">
                        <svg class="h-6 w-6 text-gray-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
                        </svg>
                    </div>
                    <div class="ml-5 w-0 flex-1">
                        <dl>
                            <dt class="text-sm font-medium text-gray-500 truncate">Total Workflows</dt>
                            <dd class="text-lg font-semibold text-gray-900">{{.WorkflowCount}}</dd>
                        </dl>
                    </div>
                </div>
            </div>
        </div>
        <div class="bg-white overflow-hidden shadow rounded-lg">
            <div class="p-5">
                <div class="flex items-center">
                    <div class="flex-shrink-0">
                        <svg class="h-6 w-6 text-blue-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z" />
                        </svg>
                    </div>
                    <div class="ml-5 w-0 flex-1">
                        <dl>
                            <dt class="text-sm font-medium text-gray-500 truncate">Running</dt>
                            <dd class="text-lg font-semibold text-blue-600">{{.Stats.Running}}</dd>
                        </dl>
                    </div>
                </div>
            </div>
        </div>
        <div class="bg-white overflow-hidden shadow rounded-lg">
            <div class="p-5">
                <div class="flex items-center">
                    <div class="flex-shrink-0">
                        <svg class="h-6 w-6 text-green-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
                        </svg>
                    </div>
                    <div class="ml-5 w-0 flex-1">
                        <dl>
                            <dt class="text-sm font-medium text-gray-500 truncate">Completed</dt>
                            <dd class="text-lg font-semibold text-green-600">{{.Stats.Completed}}</dd>
                        </dl>
                    </div>
                </div>
            </div>
        </div>
        <div class="bg-white overflow-hidden shadow rounded-lg">
            <div class="p-5">
                <div class="flex items-center">
                    <div class="flex-shrink-0">
                        <svg class="h-6 w-6 text-red-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
                        </svg>
                    </div>
                    <div class="ml-5 w-0 flex-1">
                        <dl>
                            <dt class="text-sm font-medium text-gray-500 truncate">Failed</dt>
                            <dd class="text-lg font-semibold text-red-600">{{.Stats.Failed}}</dd>
                        </dl>
                    </div>
                </div>
            </div>
        </div>
    </div>

    <div class="grid grid-cols-1 lg:grid-cols-2 gap-8">
        <!-- Recent Workflows -->
        <div class="bg-white shadow rounded-lg">
            <div class="px-4 py-5 border-b border-gray-200 sm:px-6">
                <div class="flex justify-between items-center">
                    <h3 class="text-lg leading-6 font-medium text-gray-900">Recent Workflows</h3>
                    <a href="/workflows" class="text-sm text-indigo-600 hover:text-indigo-500">View all</a>
                </div>
            </div>
            <ul class="divide-y divide-gray-200">
                {{range .RecentWorkflows}}
                <li>
                    <a href="/workflows/{{.ID}}" class="block hover:bg-gray-50 px-4 py-4">
                        <div class="flex items-center justify-between">
                            <p class="text-sm font-medium text-indigo-600 truncate">{{.Name}}</p>
                            <p class="text-xs text-gray-500">{{formatTime .CreatedAt}}</p>
                        </div>
                        <p class="mt-1 text-sm text-gray-500 truncate">{{.Description}}</p>
                    </a>
                </li>
                {{else}}
                <li class="px-4 py-4 text-sm text-gray-500">No workflows yet</li>
                {{end}}
            </ul>
        </div>

        <!-- Recent Submissions -->
        <div class="bg-white shadow rounded-lg">
            <div class="px-4 py-5 border-b border-gray-200 sm:px-6">
                <div class="flex justify-between items-center">
                    <h3 class="text-lg leading-6 font-medium text-gray-900">Recent Submissions</h3>
                    <a href="/submissions" class="text-sm text-indigo-600 hover:text-indigo-500">View all</a>
                </div>
            </div>
            <ul class="divide-y divide-gray-200">
                {{range .RecentSubmissions}}
                <li>
                    <a href="/submissions/{{.ID}}" class="block hover:bg-gray-50 px-4 py-4">
                        <div class="flex items-center justify-between">
                            <p class="text-sm font-medium text-gray-900 truncate">{{.WorkflowName}}</p>
                            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium
                                {{if eq (stateColor .State.String) "green"}}bg-green-100 text-green-800{{end}}
                                {{if eq (stateColor .State.String) "blue"}}bg-blue-100 text-blue-800{{end}}
                                {{if eq (stateColor .State.String) "yellow"}}bg-yellow-100 text-yellow-800{{end}}
                                {{if eq (stateColor .State.String) "red"}}bg-red-100 text-red-800{{end}}
                                {{if eq (stateColor .State.String) "gray"}}bg-gray-100 text-gray-800{{end}}
                            ">
                                {{.State}}
                            </span>
                        </div>
                        <p class="mt-1 text-xs text-gray-500">{{formatTime .CreatedAt}}</p>
                    </a>
                </li>
                {{else}}
                <li class="px-4 py-4 text-sm text-gray-500">No submissions yet</li>
                {{end}}
            </ul>
        </div>
    </div>

    <!-- Quick Actions -->
    <div class="mt-8">
        <h3 class="text-lg font-medium text-gray-900 mb-4">Quick Actions</h3>
        <div class="flex space-x-4">
            <a href="/workflows/new" class="inline-flex items-center px-4 py-2 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                Create Workflow
            </a>
            <a href="/submissions/new" class="inline-flex items-center px-4 py-2 border border-gray-300 text-sm font-medium rounded-md shadow-sm text-gray-700 bg-white hover:bg-gray-50">
                Submit Job
            </a>
        </div>
    </div>
</div>
{{end}}`,

	"error": `{{define "content"}}
<div class="min-h-screen flex items-center justify-center">
    <div class="text-center">
        <h1 class="text-4xl font-bold text-gray-900 mb-4">Error</h1>
        <p class="text-gray-600 mb-8">{{.Message}}</p>
        <a href="/" class="text-indigo-600 hover:text-indigo-500">Return to Dashboard</a>
    </div>
</div>
{{end}}`,

	"workflows/list": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="flex justify-between items-center mb-6">
        <h1 class="text-2xl font-semibold text-gray-900">Workflows</h1>
        <a href="/workflows/new" class="inline-flex items-center px-4 py-2 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
            Create Workflow
        </a>
    </div>

    <div class="bg-white shadow overflow-hidden sm:rounded-md">
        <ul class="divide-y divide-gray-200">
            {{range .Workflows}}
            <li id="workflow-{{.ID}}">
                <div class="px-4 py-4 sm:px-6 hover:bg-gray-50">
                    <div class="flex items-center justify-between">
                        <a href="/workflows/{{.ID}}" class="flex-1">
                            <div class="flex items-center">
                                <p class="text-sm font-medium text-indigo-600 truncate">{{.Name}}</p>
                                <span class="ml-2 inline-flex items-center px-2 py-0.5 rounded text-xs font-medium {{classBadgeColor .Class}}">
                                    {{classBadge .Class}}
                                </span>
                            </div>
                            <p class="mt-1 text-sm text-gray-500">{{.Description}}</p>
                        </a>
                        <div class="ml-4 flex items-center space-x-2">
                            <a href="/submissions/new?workflow_id={{.ID}}"
                               class="inline-flex items-center px-3 py-1 border border-gray-300 text-xs font-medium rounded text-gray-700 bg-white hover:bg-gray-50">
                                Submit
                            </a>
                            <button hx-delete="/workflows/{{.ID}}"
                                    hx-target="#workflow-{{.ID}}"
                                    hx-swap="outerHTML"
                                    hx-confirm="Are you sure you want to delete this workflow?"
                                    class="inline-flex items-center px-3 py-1 border border-red-300 text-xs font-medium rounded text-red-700 bg-white hover:bg-red-50">
                                Delete
                            </button>
                        </div>
                    </div>
                    <div class="mt-2 flex items-center text-sm text-gray-500">
                        <span>CWL {{.CWLVersion}}</span>
                        <span class="mx-2">•</span>
                        <span>{{len .Steps}} steps</span>
                        <span class="mx-2">•</span>
                        <span>Created {{formatTime .CreatedAt}}</span>
                    </div>
                </div>
            </li>
            {{else}}
            <li class="px-4 py-8 text-center text-gray-500">
                No workflows found. <a href="/workflows/new" class="text-indigo-600 hover:text-indigo-500">Create one</a>
            </li>
            {{end}}
        </ul>
    </div>

    {{if or .Pagination.HasPrev .Pagination.HasMore}}
    <div class="mt-4 flex justify-between">
        {{if .Pagination.HasPrev}}
        <a href="?offset={{.Pagination.PrevOffset}}&limit={{.Pagination.Limit}}"
           class="inline-flex items-center px-4 py-2 border border-gray-300 text-sm font-medium rounded-md text-gray-700 bg-white hover:bg-gray-50">
            Previous
        </a>
        {{else}}
        <span></span>
        {{end}}
        <span class="text-sm text-gray-500">
            Showing {{add .Pagination.Offset 1}} - {{add .Pagination.Offset (len .Workflows)}} of {{.Pagination.Total}}
        </span>
        {{if .Pagination.HasMore}}
        <a href="?offset={{.Pagination.NextOffset}}&limit={{.Pagination.Limit}}"
           class="inline-flex items-center px-4 py-2 border border-gray-300 text-sm font-medium rounded-md text-gray-700 bg-white hover:bg-gray-50">
            Next
        </a>
        {{else}}
        <span></span>
        {{end}}
    </div>
    {{end}}
</div>
{{end}}`,

	"workflows/detail": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-6">
        <div class="flex items-center justify-between">
            <div>
                <h1 class="text-2xl font-semibold text-gray-900">{{.Workflow.Name}}</h1>
                <p class="mt-1 text-sm text-gray-500">{{.Workflow.Description}}</p>
            </div>
            <div class="flex space-x-2">
                <a href="/submissions/new?workflow_id={{.Workflow.ID}}"
                   class="inline-flex items-center px-4 py-2 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                    Submit
                </a>
            </div>
        </div>
    </div>

    <div class="bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Workflow Details</h3>
        </div>
        <div class="border-t border-gray-200">
            <dl>
                <div class="bg-gray-50 px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">ID</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2 font-mono">{{.Workflow.ID}}</dd>
                </div>
                <div class="bg-white px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Type</dt>
                    <dd class="mt-1 text-sm sm:mt-0 sm:col-span-2">
                        <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium {{classBadgeColor .Workflow.Class}}">
                            {{classBadge .Workflow.Class}}
                        </span>
                        {{if isTool .Workflow.Class}}
                        <span class="ml-2 text-gray-500">(auto-wrapped as single-step workflow)</span>
                        {{end}}
                    </dd>
                </div>
                <div class="bg-gray-50 px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">CWL Version</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2">{{.Workflow.CWLVersion}}</dd>
                </div>
                <div class="bg-white px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Created</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2">{{formatTime .Workflow.CreatedAt}}</dd>
                </div>
            </dl>
        </div>
    </div>

    <!-- Inputs -->
    <div class="mt-6 bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Inputs ({{len .Workflow.Inputs}})</h3>
        </div>
        <div class="border-t border-gray-200">
            <table class="min-w-full divide-y divide-gray-200">
                <thead class="bg-gray-50">
                    <tr>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Name</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Type</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Required</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Default</th>
                    </tr>
                </thead>
                <tbody class="bg-white divide-y divide-gray-200">
                    {{range .Workflow.Inputs}}
                    <tr>
                        <td class="px-6 py-4 whitespace-nowrap text-sm font-medium text-gray-900">{{.ID}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500 font-mono">{{.Type}}</td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">
                            {{if .Required}}<span class="text-red-600">Yes</span>{{else}}No{{end}}
                        </td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500 font-mono">{{if .Default}}{{.Default}}{{else}}-{{end}}</td>
                    </tr>
                    {{else}}
                    <tr>
                        <td colspan="4" class="px-6 py-4 text-sm text-gray-500 text-center">No inputs defined</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
        </div>
    </div>

    <!-- Visual DAG -->
    {{if .Workflow.Steps}}
    <div class="mt-6 bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Workflow Graph</h3>
        </div>
        <div class="border-t border-gray-200">
            <div id="dag-container"></div>
        </div>
    </div>
    <script>
    document.addEventListener('DOMContentLoaded', function() {
        const steps = {{toJSON .Workflow.Steps}};
        if (typeof Vue !== 'undefined' && typeof DagEditor !== 'undefined') {
            const app = Vue.createApp({
                components: { DagEditor },
                data() {
                    return {
                        steps: steps
                    };
                },
                template: '<DagEditor :steps="steps" :readonly="true" />'
            });
            app.mount('#dag-container');
        }
    });
    </script>
    {{end}}

    <!-- Steps List -->
    <div class="mt-6 bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Steps ({{len .Workflow.Steps}})</h3>
        </div>
        <div class="border-t border-gray-200">
            <ul class="divide-y divide-gray-200">
                {{range .Workflow.Steps}}
                <li class="px-4 py-4">
                    <div class="flex items-center justify-between">
                        <div>
                            <p class="text-sm font-medium text-gray-900">{{.ID}}</p>
                            {{if .ToolRef}}
                            <p class="text-sm text-gray-500">Tool: {{.ToolRef}}</p>
                            {{end}}
                            {{if .Hints}}
                                {{if .Hints.BVBRCAppID}}
                                <p class="text-sm text-gray-500">BV-BRC App: {{.Hints.BVBRCAppID}}</p>
                                {{end}}
                                {{if .Hints.ExecutorType}}
                                <p class="text-sm text-gray-500">Executor: {{.Hints.ExecutorType}}</p>
                                {{end}}
                            {{end}}
                        </div>
                        {{if .DependsOn}}
                        <div class="text-sm text-gray-500">
                            Depends on: {{range $i, $dep := .DependsOn}}{{if $i}}, {{end}}{{$dep}}{{end}}
                        </div>
                        {{end}}
                    </div>
                </li>
                {{else}}
                <li class="px-4 py-4 text-sm text-gray-500 text-center">No steps defined</li>
                {{end}}
            </ul>
        </div>
    </div>

    <!-- Raw CWL -->
    <div class="mt-6 bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Raw CWL</h3>
        </div>
        <div class="border-t border-gray-200 p-4">
            <pre class="bg-gray-900 text-gray-100 p-4 rounded-lg overflow-x-auto text-sm"><code>{{.Workflow.RawCWL}}</code></pre>
        </div>
    </div>
</div>
{{end}}`,

	"workflows/create": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-6">
        <h1 class="text-2xl font-semibold text-gray-900">Create Workflow</h1>
        <p class="mt-1 text-sm text-gray-500">Upload or paste a CWL workflow definition</p>
    </div>

    {{if .Error}}
    <div class="rounded-md bg-red-50 p-4 mb-6">
        <div class="text-sm text-red-700">{{.Error}}</div>
    </div>
    {{end}}

    <form action="/api/v1/workflows" method="POST" enctype="multipart/form-data" class="space-y-6">
        <div class="bg-white shadow sm:rounded-lg">
            <div class="px-4 py-5 sm:p-6">
                <div class="space-y-6">
                    <div>
                        <label for="name" class="block text-sm font-medium text-gray-700">Workflow Name</label>
                        <input type="text" name="name" id="name"
                               class="mt-1 block w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm"
                               placeholder="My Workflow">
                    </div>

                    <div>
                        <label for="description" class="block text-sm font-medium text-gray-700">Description</label>
                        <textarea name="description" id="description" rows="3"
                                  class="mt-1 block w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm"
                                  placeholder="Optional description"></textarea>
                    </div>

                    <div>
                        <label class="block text-sm font-medium text-gray-700">CWL Definition</label>
                        <div class="mt-1">
                            <textarea name="cwl" id="cwl" rows="20"
                                      class="block w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm font-mono"
                                      placeholder="Paste CWL YAML or JSON here..."></textarea>
                        </div>
                        <p class="mt-2 text-sm text-gray-500">Paste your CWL workflow definition above, or use the API to upload a file.</p>
                    </div>
                </div>
            </div>
            <div class="px-4 py-3 bg-gray-50 text-right sm:px-6">
                <a href="/workflows" class="inline-flex justify-center py-2 px-4 border border-gray-300 shadow-sm text-sm font-medium rounded-md text-gray-700 bg-white hover:bg-gray-50 mr-3">
                    Cancel
                </a>
                <button type="submit" class="inline-flex justify-center py-2 px-4 border border-transparent shadow-sm text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-indigo-500">
                    Create Workflow
                </button>
            </div>
        </div>
    </form>
</div>
{{end}}`,

	"submissions/list": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="flex justify-between items-center mb-6">
        <h1 class="text-2xl font-semibold text-gray-900">Submissions</h1>
        <div class="flex items-center space-x-2">
            <a href="/submissions/export?format=csv{{with .StateFilter}}&amp;state={{.}}{{end}}{{with .DateStart}}&amp;date_start={{.}}{{end}}{{with .DateEnd}}&amp;date_end={{.}}{{end}}"
               class="inline-flex items-center px-3 py-2 border border-gray-300 text-sm font-medium rounded-md text-gray-700 bg-white hover:bg-gray-50">
                <svg class="h-4 w-4 mr-1" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
                </svg>
                Export CSV
            </a>
            <a href="/submissions/new" class="inline-flex items-center px-4 py-2 border border-transparent text-sm font-medium rounded-md shadow-sm text-white bg-indigo-600 hover:bg-indigo-700">
                New Submission
            </a>
        </div>
    </div>

    <!-- Filters -->
    <div class="bg-white shadow rounded-lg p-4 mb-6">
        <form method="GET" class="flex flex-wrap items-end gap-4">
            <!-- State Filter -->
            <div>
                <label class="block text-xs font-medium text-gray-500 mb-1">Status</label>
                <div class="flex space-x-1">
                    <a href="/submissions?{{with .DateStart}}date_start={{.}}{{end}}{{with .DateEnd}}&amp;date_end={{.}}{{end}}"
                       class="px-3 py-1 text-sm rounded-full {{if not .StateFilter}}bg-indigo-100 text-indigo-800{{else}}bg-gray-100 text-gray-800 hover:bg-gray-200{{end}}">
                        All
                    </a>
                    <a href="/submissions?state=PENDING{{with .DateStart}}&amp;date_start={{.}}{{end}}{{with .DateEnd}}&amp;date_end={{.}}{{end}}"
                       class="px-3 py-1 text-sm rounded-full {{if eq .StateFilter "PENDING"}}bg-yellow-100 text-yellow-800{{else}}bg-gray-100 text-gray-800 hover:bg-gray-200{{end}}">
                        Pending
                    </a>
                    <a href="/submissions?state=RUNNING{{with .DateStart}}&amp;date_start={{.}}{{end}}{{with .DateEnd}}&amp;date_end={{.}}{{end}}"
                       class="px-3 py-1 text-sm rounded-full {{if eq .StateFilter "RUNNING"}}bg-blue-100 text-blue-800{{else}}bg-gray-100 text-gray-800 hover:bg-gray-200{{end}}">
                        Running
                    </a>
                    <a href="/submissions?state=COMPLETED{{with .DateStart}}&amp;date_start={{.}}{{end}}{{with .DateEnd}}&amp;date_end={{.}}{{end}}"
                       class="px-3 py-1 text-sm rounded-full {{if eq .StateFilter "COMPLETED"}}bg-green-100 text-green-800{{else}}bg-gray-100 text-gray-800 hover:bg-gray-200{{end}}">
                        Completed
                    </a>
                    <a href="/submissions?state=FAILED{{with .DateStart}}&amp;date_start={{.}}{{end}}{{with .DateEnd}}&amp;date_end={{.}}{{end}}"
                       class="px-3 py-1 text-sm rounded-full {{if eq .StateFilter "FAILED"}}bg-red-100 text-red-800{{else}}bg-gray-100 text-gray-800 hover:bg-gray-200{{end}}">
                        Failed
                    </a>
                </div>
            </div>

            <!-- Date Range Filter -->
            <div class="flex items-end space-x-2">
                <div>
                    <label for="date_start" class="block text-xs font-medium text-gray-500 mb-1">From</label>
                    <input type="date" id="date_start" name="date_start" value="{{.DateStart}}"
                           class="block w-36 px-2 py-1 text-sm border-gray-300 rounded-md focus:ring-indigo-500 focus:border-indigo-500">
                </div>
                <div>
                    <label for="date_end" class="block text-xs font-medium text-gray-500 mb-1">To</label>
                    <input type="date" id="date_end" name="date_end" value="{{.DateEnd}}"
                           class="block w-36 px-2 py-1 text-sm border-gray-300 rounded-md focus:ring-indigo-500 focus:border-indigo-500">
                </div>
                {{if .StateFilter}}<input type="hidden" name="state" value="{{.StateFilter}}">{{end}}
                <button type="submit" class="px-3 py-1 text-sm bg-gray-100 text-gray-700 rounded-md hover:bg-gray-200">
                    Filter
                </button>
                {{if or .DateStart .DateEnd}}
                <a href="/submissions{{if .StateFilter}}?state={{.StateFilter}}{{end}}" class="px-3 py-1 text-sm text-gray-500 hover:text-gray-700">
                    Clear dates
                </a>
                {{end}}
            </div>
        </form>
    </div>

    <div class="bg-white shadow overflow-hidden sm:rounded-md">
        <ul class="divide-y divide-gray-200">
            {{range .Submissions}}
            <li>
                <a href="/submissions/{{.ID}}" class="block hover:bg-gray-50">
                    <div class="px-4 py-4 sm:px-6">
                        <div class="flex items-center justify-between">
                            <div class="flex-1 min-w-0">
                                <div class="flex items-center">
                                    <p class="text-sm font-medium text-indigo-600 truncate">{{.WorkflowName}}</p>
                                    {{if .QueuePosition}}
                                    <span class="ml-2 inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-600">
                                        Queue #{{.QueuePosition}}
                                    </span>
                                    {{end}}
                                </div>
                                <p class="mt-1 text-xs text-gray-500 font-mono">{{.ID}}</p>
                            </div>
                            <div class="ml-4 flex-shrink-0 flex items-center space-x-4">
                                <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium
                                    {{if eq (stateColor .State.String) "green"}}bg-green-100 text-green-800{{end}}
                                    {{if eq (stateColor .State.String) "blue"}}bg-blue-100 text-blue-800{{end}}
                                    {{if eq (stateColor .State.String) "yellow"}}bg-yellow-100 text-yellow-800{{end}}
                                    {{if eq (stateColor .State.String) "red"}}bg-red-100 text-red-800{{end}}
                                    {{if eq (stateColor .State.String) "gray"}}bg-gray-100 text-gray-800{{end}}
                                ">
                                    {{.State}}
                                </span>
                            </div>
                        </div>

                        <!-- Stage Pills Visualization -->
                        {{if gt .TaskSummary.Total 0}}
                        <div class="mt-3">
                            <div class="flex items-center space-x-1">
                                <!-- Task state dots -->
                                {{range .Tasks}}
                                <div class="w-3 h-3 rounded-full {{stateDotColor .State.String}}"
                                     title="{{.StepID}}: {{.State}}"></div>
                                {{end}}
                            </div>
                            <!-- Progress bar -->
                            <div class="mt-2 h-2 bg-gray-200 rounded-full overflow-hidden flex">
                                {{if gt .TaskSummary.Success 0}}
                                <div class="h-full" style="width: {{percent .TaskSummary.Success .TaskSummary.Total}}%; background: linear-gradient(to bottom, #62C462, #51A351);"></div>
                                {{end}}
                                {{if gt .TaskSummary.Running 0}}
                                <div class="h-full animate-pulse" style="width: {{percent .TaskSummary.Running .TaskSummary.Total}}%; background: linear-gradient(to bottom, #0088CC, #0044CC);"></div>
                                {{end}}
                                {{if gt .TaskSummary.Queued 0}}
                                <div class="h-full" style="width: {{percent .TaskSummary.Queued .TaskSummary.Total}}%; background: linear-gradient(to bottom, #FBB450, #F89406);"></div>
                                {{end}}
                                {{if gt .TaskSummary.Failed 0}}
                                <div class="h-full" style="width: {{percent .TaskSummary.Failed .TaskSummary.Total}}%; background: linear-gradient(to bottom, #EE5F5B, #BD362F);"></div>
                                {{end}}
                            </div>
                        </div>
                        {{end}}

                        <div class="mt-2 flex items-center text-sm text-gray-500">
                            <span>Tasks: {{.TaskSummary.Success}}/{{.TaskSummary.Total}} completed</span>
                            {{if gt .TaskSummary.Running 0}}
                            <span class="mx-2">•</span>
                            <span class="text-blue-600">{{.TaskSummary.Running}} running</span>
                            {{end}}
                            {{if gt .TaskSummary.Failed 0}}
                            <span class="mx-2">•</span>
                            <span class="text-red-600">{{.TaskSummary.Failed}} failed</span>
                            {{end}}
                            <span class="mx-2">•</span>
                            <span>{{formatTime .CreatedAt}}</span>
                        </div>
                    </div>
                </a>
            </li>
            {{else}}
            <li class="px-4 py-8 text-center text-gray-500">
                No submissions found. <a href="/submissions/new" class="text-indigo-600 hover:text-indigo-500">Create one</a>
            </li>
            {{end}}
        </ul>
    </div>

    {{if or .Pagination.HasPrev .Pagination.HasMore}}
    <div class="mt-4 flex justify-between">
        {{if .Pagination.HasPrev}}
        <a href="?offset={{.Pagination.PrevOffset}}&amp;limit={{.Pagination.Limit}}{{with .StateFilter}}&amp;state={{.}}{{end}}{{with .DateStart}}&amp;date_start={{.}}{{end}}{{with .DateEnd}}&amp;date_end={{.}}{{end}}"
           class="inline-flex items-center px-4 py-2 border border-gray-300 text-sm font-medium rounded-md text-gray-700 bg-white hover:bg-gray-50">
            Previous
        </a>
        {{else}}
        <span></span>
        {{end}}
        <span class="text-sm text-gray-500">
            Showing {{add .Pagination.Offset 1}} - {{add .Pagination.Offset (len .Submissions)}} of {{.Pagination.Total}}
        </span>
        {{if .Pagination.HasMore}}
        <a href="?offset={{.Pagination.NextOffset}}&amp;limit={{.Pagination.Limit}}{{with .StateFilter}}&amp;state={{.}}{{end}}{{with .DateStart}}&amp;date_start={{.}}{{end}}{{with .DateEnd}}&amp;date_end={{.}}{{end}}"
           class="inline-flex items-center px-4 py-2 border border-gray-300 text-sm font-medium rounded-md text-gray-700 bg-white hover:bg-gray-50">
            Next
        </a>
        {{else}}
        <span></span>
        {{end}}
    </div>
    {{end}}
</div>
{{end}}`,

	"submissions/detail": `{{define "content"}}
<div class="px-4 py-6 sm:px-0" {{if not .Submission.State.IsTerminal}}hx-trigger="every 5s" hx-get="/submissions/{{.Submission.ID}}" hx-select="main" hx-swap="outerHTML" hx-target="main"{{end}}>
    <div class="mb-6">
        <div class="flex items-center justify-between">
            <div>
                <h1 class="text-2xl font-semibold text-gray-900">{{.Submission.WorkflowName}}</h1>
                <p class="mt-1 text-sm text-gray-500 font-mono">{{.Submission.ID}}</p>
            </div>
            <div class="flex items-center space-x-2">
                {{if .Submission.QueuePosition}}
                <span class="inline-flex items-center px-3 py-1 rounded-full text-sm font-medium bg-gray-100 text-gray-700">
                    Queue Position: #{{.Submission.QueuePosition}}
                </span>
                {{end}}
                <span class="inline-flex items-center px-3 py-1 rounded-full text-sm font-medium"
                      style="{{statePillGradient .Submission.State.String}} color: white; text-shadow: 0 1px 1px rgba(0,0,0,0.2);">
                    {{.Submission.State}}
                </span>
                {{if not .Submission.State.IsTerminal}}
                <button hx-post="/submissions/{{.Submission.ID}}/cancel"
                        hx-confirm="Are you sure you want to cancel this submission?"
                        class="inline-flex items-center px-3 py-1 border border-red-300 text-sm font-medium rounded text-red-700 bg-white hover:bg-red-50">
                    Cancel
                </button>
                {{end}}
                {{if eq .Submission.State.String "FAILED"}}
                <button hx-post="/submissions/{{.Submission.ID}}/resume"
                        hx-confirm="Resume this submission from failed tasks?"
                        class="inline-flex items-center px-3 py-1 border border-green-300 text-sm font-medium rounded text-green-700 bg-white hover:bg-green-50">
                    <svg class="w-4 h-4 mr-1" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                    </svg>
                    Resume
                </button>
                {{end}}
            </div>
        </div>
    </div>

    <!-- Stage Pills Progress Bar -->
    {{if gt (len .Submission.Tasks) 0}}
    <div class="bg-white shadow rounded-lg p-4 mb-6">
        <div class="flex items-center justify-between mb-2">
            <span class="text-sm font-medium text-gray-700">Progress</span>
            <span class="text-sm text-gray-500">
                {{.Submission.TaskSummary.Success}}/{{.Submission.TaskSummary.Total}} tasks completed
            </span>
        </div>
        <!-- Stage Pills -->
        <div class="flex space-x-1 mb-3">
            {{range .Submission.Tasks}}
            <div class="flex-1 h-8 rounded flex items-center justify-center text-xs font-medium text-white cursor-pointer group relative"
                 style="{{statePillGradient .State.String}} min-width: 24px;"
                 title="{{.StepID}}: {{.State}}">
                <span class="truncate px-1">{{truncate .StepID 8}}</span>
                <!-- Tooltip -->
                <div class="absolute bottom-full left-1/2 transform -translate-x-1/2 mb-2 px-2 py-1 bg-gray-900 text-white text-xs rounded opacity-0 group-hover:opacity-100 transition-opacity whitespace-nowrap z-10">
                    {{.StepID}}: {{.State}}
                </div>
            </div>
            {{end}}
        </div>
        <!-- Legend -->
        <div class="flex flex-wrap gap-4 text-xs">
            <div class="flex items-center">
                <div class="w-3 h-3 rounded mr-1" style="background: linear-gradient(to bottom, #9CA3AF, #6B7280);"></div>
                <span>Pending</span>
            </div>
            <div class="flex items-center">
                <div class="w-3 h-3 rounded mr-1" style="background: linear-gradient(to bottom, #FBB450, #F89406);"></div>
                <span>Queued</span>
            </div>
            <div class="flex items-center">
                <div class="w-3 h-3 rounded mr-1 animate-pulse" style="background: linear-gradient(to bottom, #0088CC, #0044CC);"></div>
                <span>Running</span>
            </div>
            <div class="flex items-center">
                <div class="w-3 h-3 rounded mr-1" style="background: linear-gradient(to bottom, #62C462, #51A351);"></div>
                <span>Success</span>
            </div>
            <div class="flex items-center">
                <div class="w-3 h-3 rounded mr-1" style="background: linear-gradient(to bottom, #EE5F5B, #BD362F);"></div>
                <span>Failed</span>
            </div>
        </div>
    </div>
    {{end}}

    <!-- DAG Visualization -->
    {{if and .Workflow (gt (len .Workflow.Steps) 0)}}
    <div class="bg-white shadow overflow-hidden sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Workflow Graph</h3>
        </div>
        <div class="border-t border-gray-200">
            <div id="submission-dag-container"></div>
        </div>
    </div>
    <script>
    document.addEventListener('DOMContentLoaded', function() {
        const steps = {{toJSON .Workflow.Steps}};
        const tasks = {{toJSON .Submission.Tasks}};
        if (typeof Vue !== 'undefined' && typeof DagEditor !== 'undefined') {
            const app = Vue.createApp({
                components: { DagEditor },
                data() {
                    return {
                        steps: steps,
                        tasks: tasks
                    };
                },
                template: '<DagEditor :steps="steps" :tasks="tasks" :readonly="true" />'
            });
            app.mount('#submission-dag-container');
        }
    });
    </script>
    {{end}}

    <div class="bg-white shadow overflow-hidden sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Submission Details</h3>
        </div>
        <div class="border-t border-gray-200">
            <dl>
                <div class="bg-gray-50 px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Workflow</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2">
                        <a href="/workflows/{{.Submission.WorkflowID}}" class="text-indigo-600 hover:text-indigo-500">
                            {{.Submission.WorkflowName}}
                        </a>
                    </dd>
                </div>
                <div class="bg-white px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Submitted By</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2">{{.Submission.SubmittedBy}}</dd>
                </div>
                <div class="bg-gray-50 px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Created</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2">{{formatTime .Submission.CreatedAt}}</dd>
                </div>
                {{if .Submission.CompletedAt}}
                <div class="bg-white px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Completed</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2">{{formatTimePtr .Submission.CompletedAt}}</dd>
                </div>
                {{end}}
            </dl>
        </div>
    </div>

    <!-- Tasks -->
    <div class="bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6 flex items-center justify-between">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Tasks ({{len .Submission.Tasks}})</h3>
            {{if eq .Submission.State.String "FAILED"}}
            <div class="flex space-x-2">
                <button hx-post="/submissions/{{.Submission.ID}}/recompute-failed"
                        hx-confirm="Recompute all failed tasks?"
                        class="inline-flex items-center px-3 py-1 text-xs border border-orange-300 rounded text-orange-700 bg-white hover:bg-orange-50">
                    Recompute Failed
                </button>
            </div>
            {{end}}
        </div>
        <div class="border-t border-gray-200">
            <table class="min-w-full divide-y divide-gray-200">
                <thead class="bg-gray-50">
                    <tr>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Step</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Status</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Executor</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Duration</th>
                        <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Actions</th>
                    </tr>
                </thead>
                <tbody class="bg-white divide-y divide-gray-200">
                    {{range .Submission.Tasks}}
                    <tr class="hover:bg-gray-50">
                        <td class="px-6 py-4 whitespace-nowrap">
                            <div class="flex items-center">
                                <div class="w-3 h-3 rounded-full mr-2 {{stateDotColor .State.String}}"></div>
                                <div>
                                    <div class="text-sm font-medium text-gray-900">{{.StepID}}</div>
                                    <div class="text-xs text-gray-500 font-mono">{{.ID}}</div>
                                </div>
                            </div>
                        </td>
                        <td class="px-6 py-4 whitespace-nowrap">
                            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium text-white"
                                  style="{{statePillGradient .State.String}}">
                                {{.State}}
                            </span>
                            {{if .ExitCode}}
                            <span class="ml-2 text-xs text-gray-500">Exit: {{.ExitCode}}</span>
                            {{end}}
                        </td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">
                            {{.ExecutorType}}
                            {{if .BVBRCAppID}}
                            <br><span class="text-xs">{{.BVBRCAppID}}</span>
                            {{end}}
                        </td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">
                            {{if .StartedAt}}
                                {{if .CompletedAt}}
                                    {{formatDuration .StartedAt}}
                                {{else}}
                                    <span class="text-blue-600">Running...</span>
                                {{end}}
                            {{else}}
                                -
                            {{end}}
                        </td>
                        <td class="px-6 py-4 whitespace-nowrap text-sm space-x-2">
                            {{if or .Stdout .Stderr}}
                            <a href="/submissions/{{$.Submission.ID}}/tasks/{{.ID}}/logs"
                               class="text-indigo-600 hover:text-indigo-500">
                                Logs
                            </a>
                            {{end}}
                            {{if eq .State.String "FAILED"}}
                            <button hx-post="/submissions/{{$.Submission.ID}}/tasks/{{.ID}}/recompute"
                                    hx-confirm="Recompute this task from the beginning?"
                                    class="text-orange-600 hover:text-orange-500">
                                Recompute
                            </button>
                            {{end}}
                        </td>
                    </tr>
                    {{else}}
                    <tr>
                        <td colspan="5" class="px-6 py-4 text-sm text-gray-500 text-center">No tasks</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
        </div>
    </div>

    <!-- Inputs -->
    {{if .Submission.Inputs}}
    <div class="mt-6 bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Inputs</h3>
        </div>
        <div class="border-t border-gray-200 p-4">
            <pre class="bg-gray-900 text-gray-100 p-4 rounded-lg overflow-x-auto text-sm"><code>{{json .Submission.Inputs}}</code></pre>
        </div>
    </div>
    {{end}}

    <!-- Outputs -->
    {{if .Submission.Outputs}}
    <div class="mt-6 bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Outputs</h3>
        </div>
        <div class="border-t border-gray-200 p-4">
            <pre class="bg-gray-900 text-gray-100 p-4 rounded-lg overflow-x-auto text-sm"><code>{{json .Submission.Outputs}}</code></pre>
        </div>
    </div>
    {{end}}
</div>
{{end}}`,

	"submissions/create": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-6">
        <h1 class="text-2xl font-semibold text-gray-900">Submit Workflow</h1>
        <p class="mt-1 text-sm text-gray-500">Select a workflow and provide input values</p>
    </div>

    {{if .Error}}
    <div class="rounded-md bg-red-50 p-4 mb-6">
        <div class="text-sm text-red-700">{{.Error}}</div>
    </div>
    {{end}}

    <form action="/api/v1/submissions" method="POST" class="space-y-6">
        <div class="bg-white shadow sm:rounded-lg">
            <div class="px-4 py-5 sm:p-6">
                <div class="space-y-6">
                    <div>
                        <label for="workflow_id" class="block text-sm font-medium text-gray-700">Workflow</label>
                        <select name="workflow_id" id="workflow_id" required
                                onchange="if(this.value) window.location.href='/submissions/new?workflow_id='+this.value"
                                class="mt-1 block w-full pl-3 pr-10 py-2 text-base border-gray-300 focus:outline-none focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm rounded-md">
                            <option value="">Select a workflow...</option>
                            {{range .Workflows}}
                            <option value="{{.ID}}" {{if and $.SelectedWorkflow (eq $.SelectedWorkflow.ID .ID)}}selected{{end}}>
                                {{.Name}}
                            </option>
                            {{end}}
                        </select>
                    </div>

                    {{if .SelectedWorkflow}}
                    <div>
                        <h4 class="text-sm font-medium text-gray-700 mb-2">Workflow Inputs</h4>
                        {{range .SelectedWorkflow.Inputs}}
                        <div class="mb-4">
                            <label for="input_{{.ID}}" class="block text-sm font-medium text-gray-700">
                                {{.ID}}
                                {{if .Required}}<span class="text-red-500">*</span>{{end}}
                                <span class="text-gray-400 font-normal">({{.Type}})</span>
                            </label>
                            {{if isFileType .Type}}
                            <!-- File input with workspace picker -->
                            <div class="mt-1 flex items-center space-x-2">
                                <input type="text" name="inputs[{{.ID}}]" id="input_{{.ID}}"
                                       {{if .Required}}required{{end}}
                                       placeholder="/username@bvbrc/home/path/to/file"
                                       class="flex-1 border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm font-mono">
                                {{if $.HasWorkspace}}
                                <button type="button"
                                        onclick="GoWe.FilePicker.open('input_{{.ID}}', '{{$.WorkspacePath}}')"
                                        class="inline-flex items-center px-3 py-2 border border-gray-300 shadow-sm text-sm font-medium rounded-md text-gray-700 bg-white hover:bg-gray-50">
                                    📁 Browse
                                </button>
                                {{end}}
                            </div>
                            <p class="mt-1 text-xs text-gray-500">{{if $.HasWorkspace}}Enter a workspace path or browse to select a file{{else}}Enter a BV-BRC workspace path (e.g., /user@bvbrc/home/file.fasta){{end}}</p>
                            {{else if isArrayType .Type}}
                            <!-- Array input -->
                            <textarea name="inputs[{{.ID}}]" id="input_{{.ID}}" rows="3"
                                      {{if .Required}}required{{end}}
                                      class="mt-1 block w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm font-mono"
                                      placeholder="One value per line, or JSON array"></textarea>
                            <p class="mt-1 text-xs text-gray-500">Enter one value per line, or a JSON array</p>
                            {{else}}
                            <!-- Standard text input -->
                            <input type="text" name="inputs[{{.ID}}]" id="input_{{.ID}}"
                                   {{if .Required}}required{{end}}
                                   {{if .Default}}value="{{.Default}}"{{end}}
                                   class="mt-1 block w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm">
                            {{end}}
                        </div>
                        {{end}}
                    </div>
                    {{else}}
                    <p class="text-sm text-gray-500">Select a workflow to see its inputs.</p>
                    {{end}}

                    <div>
                        <label for="labels" class="block text-sm font-medium text-gray-700">Labels (optional)</label>
                        <textarea name="labels" id="labels" rows="2"
                                  class="mt-1 block w-full border-gray-300 rounded-md shadow-sm focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm font-mono"
                                  placeholder='{"environment": "production"}'></textarea>
                        <p class="mt-1 text-xs text-gray-500">JSON object with key-value pairs for organization</p>
                    </div>
                </div>
            </div>
            <div class="px-4 py-3 bg-gray-50 text-right sm:px-6">
                <a href="/submissions" class="inline-flex justify-center py-2 px-4 border border-gray-300 shadow-sm text-sm font-medium rounded-md text-gray-700 bg-white hover:bg-gray-50 mr-3">
                    Cancel
                </a>
                <button type="submit" class="inline-flex justify-center py-2 px-4 border border-transparent shadow-sm text-sm font-medium rounded-md text-white bg-indigo-600 hover:bg-indigo-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-indigo-500">
                    Submit
                </button>
            </div>
        </div>
    </form>
</div>
{{end}}`,

	"submissions/task_logs": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-6">
        <div class="flex items-center space-x-2 text-sm text-gray-500 mb-2">
            <a href="/submissions/{{.SubmissionID}}" class="hover:text-gray-700">Submission</a>
            <span>/</span>
            <span>Task Logs</span>
        </div>
        <h1 class="text-2xl font-semibold text-gray-900">{{.Task.StepID}}</h1>
        <p class="mt-1 text-sm text-gray-500 font-mono">{{.Task.ID}}</p>
    </div>

    <!-- Stdout -->
    <div class="bg-white shadow overflow-hidden sm:rounded-lg mb-6">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Standard Output</h3>
        </div>
        <div class="border-t border-gray-200 p-4">
            {{if .Task.Stdout}}
            <pre class="bg-gray-900 text-gray-100 p-4 rounded-lg overflow-x-auto text-sm max-h-96"><code>{{.Task.Stdout}}</code></pre>
            {{else}}
            <p class="text-gray-500 text-sm">No output</p>
            {{end}}
        </div>
    </div>

    <!-- Stderr -->
    <div class="bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Standard Error</h3>
        </div>
        <div class="border-t border-gray-200 p-4">
            {{if .Task.Stderr}}
            <pre class="bg-red-900 text-red-100 p-4 rounded-lg overflow-x-auto text-sm max-h-96"><code>{{.Task.Stderr}}</code></pre>
            {{else}}
            <p class="text-gray-500 text-sm">No errors</p>
            {{end}}
        </div>
    </div>
</div>
{{end}}`,

	"admin/stats": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">System Statistics</h1>
        <p class="mt-1 text-sm text-gray-500">Overview of GoWe system status</p>
    </div>

    <!-- Stats Grid -->
    <div class="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-4 mb-8">
        <div class="bg-white overflow-hidden shadow rounded-lg">
            <div class="p-5">
                <div class="flex items-center">
                    <div class="flex-shrink-0">
                        <svg class="h-6 w-6 text-gray-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
                        </svg>
                    </div>
                    <div class="ml-5 w-0 flex-1">
                        <dl>
                            <dt class="text-sm font-medium text-gray-500 truncate">Total Workflows</dt>
                            <dd class="text-lg font-semibold text-gray-900">{{.WorkflowCount}}</dd>
                        </dl>
                    </div>
                </div>
            </div>
        </div>

        <div class="bg-white overflow-hidden shadow rounded-lg">
            <div class="p-5">
                <div class="flex items-center">
                    <div class="flex-shrink-0">
                        <svg class="h-6 w-6 text-gray-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10" />
                        </svg>
                    </div>
                    <div class="ml-5 w-0 flex-1">
                        <dl>
                            <dt class="text-sm font-medium text-gray-500 truncate">Total Submissions</dt>
                            <dd class="text-lg font-semibold text-gray-900">{{.SubmissionCount}}</dd>
                        </dl>
                    </div>
                </div>
            </div>
        </div>

        <div class="bg-white overflow-hidden shadow rounded-lg">
            <div class="p-5">
                <div class="flex items-center">
                    <div class="flex-shrink-0">
                        <svg class="h-6 w-6 text-green-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
                        </svg>
                    </div>
                    <div class="ml-5 w-0 flex-1">
                        <dl>
                            <dt class="text-sm font-medium text-gray-500 truncate">Uptime</dt>
                            <dd class="text-lg font-semibold text-gray-900">{{.Uptime}}</dd>
                        </dl>
                    </div>
                </div>
            </div>
        </div>

        <div class="bg-white overflow-hidden shadow rounded-lg">
            <div class="p-5">
                <div class="flex items-center">
                    <div class="flex-shrink-0">
                        <svg class="h-6 w-6 text-blue-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z" />
                        </svg>
                    </div>
                    <div class="ml-5 w-0 flex-1">
                        <dl>
                            <dt class="text-sm font-medium text-gray-500 truncate">Running</dt>
                            <dd class="text-lg font-semibold text-blue-600">{{.SubmissionStats.Running}}</dd>
                        </dl>
                    </div>
                </div>
            </div>
        </div>
    </div>

    <!-- Submission Stats -->
    <div class="bg-white shadow rounded-lg">
        <div class="px-4 py-5 border-b border-gray-200 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Submission Statistics</h3>
        </div>
        <div class="p-6">
            <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
                <div class="text-center p-4 bg-yellow-50 rounded-lg">
                    <p class="text-2xl font-bold text-yellow-600">{{.SubmissionStats.Pending}}</p>
                    <p class="text-sm text-yellow-800">Pending</p>
                </div>
                <div class="text-center p-4 bg-blue-50 rounded-lg">
                    <p class="text-2xl font-bold text-blue-600">{{.SubmissionStats.Running}}</p>
                    <p class="text-sm text-blue-800">Running</p>
                </div>
                <div class="text-center p-4 bg-green-50 rounded-lg">
                    <p class="text-2xl font-bold text-green-600">{{.SubmissionStats.Completed}}</p>
                    <p class="text-sm text-green-800">Completed</p>
                </div>
                <div class="text-center p-4 bg-red-50 rounded-lg">
                    <p class="text-2xl font-bold text-red-600">{{.SubmissionStats.Failed}}</p>
                    <p class="text-sm text-red-800">Failed</p>
                </div>
            </div>
        </div>
    </div>
</div>
{{end}}`,

	"admin/health": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-8">
        <h1 class="text-2xl font-semibold text-gray-900">System Health</h1>
        <p class="mt-1 text-sm text-gray-500">GoWe server health status</p>
    </div>

    <div class="bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Server Status</h3>
        </div>
        <div class="border-t border-gray-200">
            <dl>
                <div class="bg-gray-50 px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Status</dt>
                    <dd class="mt-1 text-sm sm:mt-0 sm:col-span-2">
                        <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
                            Healthy
                        </span>
                    </dd>
                </div>
                <div class="bg-white px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Uptime</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2">{{.Uptime}}</dd>
                </div>
                <div class="bg-gray-50 px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">Start Time</dt>
                    <dd class="mt-1 text-sm text-gray-900 sm:mt-0 sm:col-span-2">{{.StartTime}}</dd>
                </div>
                <div class="bg-white px-4 py-5 sm:grid sm:grid-cols-3 sm:gap-4 sm:px-6">
                    <dt class="text-sm font-medium text-gray-500">BV-BRC Connection</dt>
                    <dd class="mt-1 text-sm sm:mt-0 sm:col-span-2">
                        {{if .HasBVBRC}}
                        <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
                            Connected
                        </span>
                        {{else}}
                        <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-yellow-100 text-yellow-800">
                            Not Configured
                        </span>
                        {{end}}
                    </dd>
                </div>
            </dl>
        </div>
    </div>

    <!-- Executors -->
    <div class="mt-6 bg-white shadow overflow-hidden sm:rounded-lg">
        <div class="px-4 py-5 sm:px-6">
            <h3 class="text-lg leading-6 font-medium text-gray-900">Executors</h3>
        </div>
        <div class="border-t border-gray-200">
            <ul class="divide-y divide-gray-200">
                <li class="px-4 py-4 flex items-center justify-between">
                    <div>
                        <p class="text-sm font-medium text-gray-900">Local Executor</p>
                        <p class="text-sm text-gray-500">Runs tasks on the server host</p>
                    </div>
                    <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
                        Available
                    </span>
                </li>
                <li class="px-4 py-4 flex items-center justify-between">
                    <div>
                        <p class="text-sm font-medium text-gray-900">Docker Executor</p>
                        <p class="text-sm text-gray-500">Runs tasks in containers</p>
                    </div>
                    <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
                        Available
                    </span>
                </li>
                <li class="px-4 py-4 flex items-center justify-between">
                    <div>
                        <p class="text-sm font-medium text-gray-900">BV-BRC Executor</p>
                        <p class="text-sm text-gray-500">Runs tasks on BV-BRC servers</p>
                    </div>
                    {{if .HasBVBRC}}
                    <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
                        Available
                    </span>
                    {{else}}
                    <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-yellow-100 text-yellow-800">
                        No Token
                    </span>
                    {{end}}
                </li>
            </ul>
        </div>
    </div>
</div>
{{end}}`,

	"workspace/browser": `{{define "content"}}
<div class="px-4 py-6 sm:px-0">
    <div class="mb-6">
        <h1 class="text-2xl font-semibold text-gray-900">Workspace</h1>
        <p class="mt-1 text-sm text-gray-500 font-mono">{{.Path}}</p>
    </div>

    <!-- Breadcrumb -->
    <nav class="flex mb-4" aria-label="Breadcrumb">
        <ol class="flex items-center space-x-2">
            <li>
                <a href="/workspace" class="text-gray-500 hover:text-gray-700">Home</a>
            </li>
            <!-- TODO: Build path segments -->
        </ol>
    </nav>

    <div class="bg-white shadow overflow-hidden sm:rounded-lg">
        <ul class="divide-y divide-gray-200">
            {{range .Items}}
            {{if gt (len .) 0}}
            {{$name := index . 0}}
            {{$type := index . 1}}
            {{$parent := index . 2}}
            {{$fullPath := printf "%s%s" $parent $name}}
            <li>
                {{if eq $type "folder"}}
                <a href="/workspace?path={{urlquery $fullPath}}" class="block hover:bg-gray-50 px-4 py-4">
                    <div class="flex items-center">
                        <svg class="h-5 w-5 text-yellow-400 mr-3" fill="currentColor" viewBox="0 0 24 24">
                            <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z" />
                        </svg>
                        <span class="text-sm font-medium text-gray-900">{{$name}}</span>
                    </div>
                </a>
                {{else}}
                <div class="block px-4 py-4">
                    <div class="flex items-center">
                        <svg class="h-5 w-5 text-gray-400 mr-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M7 21h10a2 2 0 002-2V9.414a1 1 0 00-.293-.707l-5.414-5.414A1 1 0 0012.586 3H7a2 2 0 00-2 2v14a2 2 0 002 2z" />
                        </svg>
                        <span class="text-sm text-gray-900">{{$name}}</span>
                        <span class="ml-2 text-xs text-gray-500">{{$type}}</span>
                    </div>
                </div>
                {{end}}
            </li>
            {{end}}
            {{else}}
            <li class="px-4 py-8 text-center text-gray-500">
                Empty directory
            </li>
            {{end}}
        </ul>
    </div>
</div>
{{end}}`,
}
