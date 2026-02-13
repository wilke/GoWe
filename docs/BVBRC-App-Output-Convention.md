# BV-BRC App Output Convention

Documented 2026-02-12 by inspecting the BV-BRC app framework source code.

## Overview

Every BV-BRC app job produces two workspace objects:

```
{output_path}/
    {output_file}           <-- job_result typed object (JSON metadata)
    .{output_file}/         <-- hidden folder (actual output files)
        file1.txt
        file2.tsv
        JobFailed.txt       <-- only present if the job failed
```

- **`{output_path}/{output_file}`** -- A workspace object of type `job_result` containing JSON metadata about the job (timing, parameters, output file manifest, success/failure).
- **`{output_path}/.{output_file}/`** -- A dot-prefixed (hidden) workspace folder containing the actual output files produced by the app.

There is no formal documentation of this convention. It is established through code in `AppScript.pm` and relied upon by the web UI.

## Source References

| File | Purpose |
|------|---------|
| `dev_container/modules/app_service/lib/Bio/KBase/AppService/AppScript.pm` | Framework: folder creation, job lifecycle, result writing |
| `bvbrc_standalone_apps/service-scripts/App-*.pl` | App implementations |
| `BV-BRC-Web/public/js/p3/widget/viewer/JobResult.js` | Web UI: job result viewer |
| `BV-BRC-Web/public/js/p3/WorkspaceManager.js` | Web UI: workspace operations |

## App Lifecycle

The framework (`AppScript.pm:subproc_run`) runs these steps in order:

1. **`initialize_workspace()`** -- Create workspace client with auth token.
2. **`setup_folders()`** -- Call `create_result_folder()` (unless `donot_create_result_folder` is set).
3. **Execute callback** -- The app's main function runs. App writes output files into `$app->result_folder()`.
4. **`write_results()`** -- Enumerate output files, create the `job_result` object, optionally write `JobFailed.*`.

## `create_result_folder()`

```perl
# AppScript.pm:572-594
sub create_result_folder {
    my($self) = @_;
    my $base_folder   = $self->params->{output_path};
    my $result_folder = $base_folder . "/." . $self->params->{output_file};
    $self->result_folder($result_folder);
    $self->workspace->create({
        overwrite => 1,
        objects   => [[$result_folder, 'folder', { application_type => $self->app_definition->{id} }]]
    });
    # ... cleanup prior JobFailed.* files
}
```

**Formula:** `result_folder = output_path + "/." + output_file`

The folder is created with metadata `{ application_type: <app_id> }` and `overwrite: 1`.

## `write_results()`

```perl
# AppScript.pm:775-832
sub write_results {
    my($self, $job_output, $success, $failure_report) = @_;

    # 1. List all files in the hidden result folder
    my $files = $self->workspace->ls({ paths => [$self->result_folder], recursive => 1 });

    # 2. Build the job_result JSON
    my $job_obj = {
        id           => $self->task_id,
        app          => $self->app_definition,
        parameters   => $self->params,
        start_time   => $start_time,
        end_time     => $end_time,
        elapsed_time => $elap,
        hostname     => $self->host,
        output_files => [ map { [$_->[2] . $_->[0], $_->[4]] } @{$files->{$self->result_folder}} ],
        job_output   => $job_output,
        success      => ($success ? 1 : 0),
    };

    # 3. Save at output_path/output_file (no dot prefix)
    my $file = $self->params->{output_path} . "/" . $self->params->{output_file};
    $self->workspace->save_data_to_file($json->encode($job_obj), $meta, $file, 'job_result', 1);

    # 4. On failure, write report into the hidden folder
    if ($failure_report) {
        $self->workspace->save_data_to_file($failure_report, {},
            $self->result_folder . "/JobFailed.$type", $type, 1);
    }
}
```

## job_result Object Structure

The `job_result` JSON (at `{output_path}/{output_file}`) contains:

```json
{
    "id": "21463335",
    "app": {
        "id": "Date",
        "script": "App-Date",
        "label": "Date",
        "description": "Returns the current date and time.",
        "parameters": [ ... ]
    },
    "parameters": {
        "output_path": "/user@bvbrc/home/gowe-test",
        "output_file": "test-pipeline"
    },
    "start_time": 1770937295.938,
    "end_time": 1770937296.296,
    "elapsed_time": 0.358,
    "hostname": "coconut",
    "output_files": [
        ["/user@bvbrc/home/gowe-test/.test-pipeline/now", "C826F096-0866-11F1-ADE7-9D52B79EE16D"]
    ],
    "job_output": "...",
    "success": 1
}
```

The `output_files` array is the authoritative manifest of all output files, each as `[workspace_path, object_uuid]`.

The object's workspace metadata includes:

```json
{
    "task_data": {
        "success": 1,
        "task_id": "21463335",
        "start_time": 1770937295.938,
        "end_time": 1770937296.296,
        "elapsed_time": 0.358,
        "app_id": "Date"
    }
}
```

## Opt-Out Flags

Apps can opt out of the framework's output handling:

| Flag | Effect | Used By |
|------|--------|---------|
| `donot_create_result_folder` | Skip creating the hidden folder | Sleep (no output) |
| `donot_create_job_result` | Skip creating the job_result object | FluxBalanceAnalysis (helper manages output) |
| `skip_workspace_output` (parameter) | Sets both flags | User-specified per job |

## How Apps Write Output

Apps obtain the hidden folder path via `$app->result_folder()` and save files there:

```perl
# Date app -- saves one file
my $folder = $app->result_folder();
$app->workspace->save_data_to_file($date, {}, "$folder/now", 'txt', 1);

# GenomeComparison -- saves many files
my $output_folder = $app->result_folder();
$app->workspace->save_file_to_file($local, {}, "$output_folder/$name", $type, 1, 0, $token);
```

Apps never use `output_path`/`output_file` directly for file output -- they always go through `result_folder()`.

## Web UI Handling

The `JobResult.js` viewer reconstructs the hidden path:

```javascript
// JobResult.js:51
this._hiddenPath = data.path + '.' + data.name;
```

The `WorkspaceManager` handles move/copy/delete of both the job_result object and its hidden folder together, with the comment:

```javascript
// hack to deal with job result data (dot folders)
```

Known issue: downloaded zip files contain the dot-prefixed folder, which is hidden by default on Unix/macOS.

## Workspace API Methods

| Method | Signature | Notes |
|--------|-----------|-------|
| `save_data_to_file` | `($data, $meta, $path, $type, $overwrite, $use_shock, $token)` | Saves string data inline (or via Shock for large data) |
| `save_file_to_file` | `($local_path, $meta, $ws_path, $type, $overwrite, $use_shock, $token)` | Uploads a local file |
| `create` | `({objects => [[$path, $type, $meta]], overwrite => 0/1})` | Low-level workspace object creation |

`save_data_to_file` does **not** auto-create parent folders. However, the workspace server itself auto-creates intermediate folders when creating objects.

## Implications for GoWe

### Output Resolution

When a BV-BRC task completes, GoWe can resolve outputs by:

1. **Reading the `job_result` object** at `{output_path}/{output_file}` via `Workspace.get` -- this contains the `output_files` manifest with exact workspace paths.
2. **Listing the hidden folder** at `{output_path}/.{output_file}/` via `Workspace.ls` -- this returns all output files directly.

Option 1 is preferred because the `job_result` already includes file UUIDs, types, and the complete path.

### CWL Glob Pattern

The current generator produces:

```yaml
glob: $(inputs.output_path.location)/$(inputs.output_file)*
```

This matches the **job_result metadata document**, not the actual output files. To match output files in the hidden folder:

```yaml
# Option A: glob the hidden folder contents
glob: $(inputs.output_path.location)/.$(inputs.output_file)/*

# Option B: reference the job_result metadata (current behavior)
glob: $(inputs.output_path.location)/$(inputs.output_file)
```

For GoWe's BV-BRC executor, the glob pattern is not evaluated by a filesystem -- it's interpreted by the output resolver. The resolver should understand the BV-BRC convention and:

1. Fetch the `job_result` object after task completion
2. Parse `output_files` to build the output manifest
3. Map CWL output IDs to workspace files

### Framework Parameters

`output_path` and `output_file` are framework-level parameters present in all BV-BRC apps (35/39 have `output_path` as type `folder`). They are:
- **Not consumed by app code** -- only by the framework's `create_result_folder()` and `write_results()`
- **Always present** -- even apps without explicit parameters get them injected
- **Required for output resolution** -- without them, GoWe cannot locate job outputs

The CWL tool generator should always include these as inputs, and the GoWe executor should always pass them to `start_app`.
