"""BV-BRC JSON-RPC 1.1 Client."""

import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import httpx

DEFAULT_APP_SERVICE_URL = "https://p3.theseed.org/services/app_service"
DEFAULT_WORKSPACE_URL = "https://p3.theseed.org/services/Workspace"


@dataclass
class Task:
    """BV-BRC job/task."""

    id: str
    app: str
    owner: str
    status: str
    submit_time: str | None = None
    start_time: str | None = None
    completed_time: str | None = None
    parameters: dict[str, Any] | None = None
    output_path: str | None = None


@dataclass
class AppDescription:
    """BV-BRC application description."""

    id: str
    label: str
    description: str
    parameters: list[dict[str, Any]] | None = None


@dataclass
class WorkspaceObject:
    """BV-BRC workspace object."""

    path: str
    type: str
    owner: str
    creation_time: str
    id: str
    size: int
    user_metadata: dict[str, str]
    auto_metadata: dict[str, str]
    shock_ref: str | None = None
    data: str | None = None


class BVBRCClient:
    """Client for BV-BRC JSON-RPC API."""

    def __init__(
        self,
        token: str | None = None,
        app_service_url: str = DEFAULT_APP_SERVICE_URL,
        workspace_url: str = DEFAULT_WORKSPACE_URL,
    ):
        self.app_service_url = app_service_url
        self.workspace_url = workspace_url
        self.token = token or _load_token()
        self._request_id = 0
        self._http = httpx.Client(timeout=30.0)

    @property
    def username(self) -> str:
        """Extract username from token."""
        if not self.token:
            return ""
        for part in self.token.split("|"):
            if part.startswith("un="):
                return part[3:]
        return ""

    @property
    def is_authenticated(self) -> bool:
        """Check if client has a token."""
        return bool(self.token)

    # --- App Service Methods ---

    def enumerate_apps(self) -> list[AppDescription]:
        """List all available applications."""
        result = self._call_app_service("AppService.enumerate_apps", [])
        apps = result[0] if result else []
        return [
            AppDescription(
                id=a.get("id", ""),
                label=a.get("label", ""),
                description=a.get("description", ""),
                parameters=a.get("parameters"),
            )
            for a in apps
        ]

    def query_app_description(self, app_id: str) -> AppDescription | None:
        """Get detailed app description."""
        result = self._call_app_service("AppService.query_app_description", [app_id])
        if not result:
            return None
        a = result[0]
        return AppDescription(
            id=a.get("id", ""),
            label=a.get("label", ""),
            description=a.get("description", ""),
            parameters=a.get("parameters"),
        )

    def start_app(
        self, app_id: str, params: dict[str, Any], output_path: str
    ) -> Task:
        """Submit a new job."""
        result = self._call_app_service(
            "AppService.start_app", [app_id, params, output_path]
        )
        t = result[0]
        return Task(
            id=t.get("id", ""),
            app=t.get("app", ""),
            owner=t.get("owner", ""),
            status=t.get("status", ""),
            submit_time=t.get("submit_time"),
            start_time=t.get("start_time"),
            completed_time=t.get("completed_time"),
            parameters=t.get("parameters"),
            output_path=t.get("output_path"),
        )

    def query_tasks(self, task_ids: list[str]) -> dict[str, Task]:
        """Query task status."""
        result = self._call_app_service("AppService.query_tasks", [task_ids])
        if not result:
            return {}
        tasks = {}
        for tid, t in result[0].items():
            tasks[tid] = Task(
                id=t.get("id", ""),
                app=t.get("app", ""),
                owner=t.get("owner", ""),
                status=t.get("status", ""),
                submit_time=t.get("submit_time"),
                start_time=t.get("start_time"),
                completed_time=t.get("completed_time"),
                parameters=t.get("parameters"),
                output_path=t.get("output_path"),
            )
        return tasks

    def enumerate_tasks(self, offset: int, limit: int) -> list[Task]:
        """List tasks with pagination."""
        result = self._call_app_service("AppService.enumerate_tasks", [offset, limit])
        tasks_raw = result[0] if result else []
        return [
            Task(
                id=t.get("id", ""),
                app=t.get("app", ""),
                owner=t.get("owner", ""),
                status=t.get("status", ""),
                submit_time=t.get("submit_time"),
                start_time=t.get("start_time"),
                completed_time=t.get("completed_time"),
                parameters=t.get("parameters"),
                output_path=t.get("output_path"),
            )
            for t in tasks_raw
        ]

    def kill_task(self, task_id: str) -> bool:
        """Cancel a task."""
        result = self._call_app_service("AppService.kill_task", [task_id])
        return result[0] == 1 if result else False

    def query_app_log(self, task_id: str) -> str:
        """Get task logs."""
        result = self._call_app_service("AppService.query_app_log", [task_id])
        if isinstance(result, str):
            return result
        if isinstance(result, list) and result:
            return str(result[0])
        return ""

    # --- Workspace Methods ---

    def workspace_ls(
        self, paths: list[str], recursive: bool = False
    ) -> dict[str, list[WorkspaceObject]]:
        """List workspace directory."""
        params: dict[str, Any] = {"paths": paths}
        if recursive:
            params["recursive"] = True

        result = self._call_workspace("Workspace.ls", [params])
        if not result:
            return {}

        parsed: dict[str, list[WorkspaceObject]] = {}
        for path, tuples in result[0].items():
            parsed[path] = [_parse_workspace_object(t) for t in tuples]
        return parsed

    def workspace_get(
        self, paths: list[str], metadata_only: bool = False
    ) -> list[WorkspaceObject]:
        """Get workspace objects."""
        params: dict[str, Any] = {"objects": paths}
        if metadata_only:
            params["metadata_only"] = True

        result = self._call_workspace("Workspace.get", [params])
        if not result or not result[0]:
            return []
        return [_parse_workspace_object(t) for t in result[0]]

    def workspace_create(
        self,
        path: str,
        obj_type: str,
        content: str | None = None,
        overwrite: bool = False,
    ) -> WorkspaceObject:
        """Create workspace object."""
        obj_spec = [path, obj_type, {}, content]
        params: dict[str, Any] = {"objects": [obj_spec]}
        if overwrite:
            params["overwrite"] = True

        result = self._call_workspace("Workspace.create", [params])
        return _parse_workspace_object(result[0][0])

    def workspace_delete(self, paths: list[str], force: bool = False) -> None:
        """Delete workspace objects."""
        params: dict[str, Any] = {"objects": paths, "deleteDirectories": True}
        if force:
            params["force"] = True
        self._call_workspace("Workspace.delete", [params])

    def workspace_copy(
        self, source: str, destination: str
    ) -> list[WorkspaceObject]:
        """Copy workspace object."""
        params = {"objects": [[source, destination]], "recursive": True}
        result = self._call_workspace("Workspace.copy", [params])
        if not result or not result[0]:
            return []
        return [_parse_workspace_object(t) for t in result[0]]

    def workspace_move(
        self, source: str, destination: str
    ) -> list[WorkspaceObject]:
        """Move workspace object."""
        params = {"objects": [[source, destination]]}
        result = self._call_workspace("Workspace.move", [params])
        if not result or not result[0]:
            return []
        return [_parse_workspace_object(t) for t in result[0]]

    def workspace_set_permissions(
        self, path: str, permissions: list[tuple[str, str]]
    ) -> None:
        """Set workspace permissions."""
        params = {"path": path, "permissions": permissions}
        self._call_workspace("Workspace.set_permissions", [params])

    def workspace_get_download_url(self, paths: list[str]) -> dict[str, str]:
        """Get download URLs."""
        params = {"objects": paths}
        result = self._call_workspace("Workspace.get_download_url", [params])
        return result[0] if result else {}

    # --- RPC Helpers ---

    def _call_app_service(self, method: str, params: list[Any]) -> Any:
        return self._rpc_call(self.app_service_url, method, params)

    def _call_workspace(self, method: str, params: list[Any]) -> Any:
        return self._rpc_call(self.workspace_url, method, params)

    def _rpc_call(self, url: str, method: str, params: list[Any]) -> Any:
        self._request_id += 1
        request = {
            "id": str(self._request_id),
            "method": method,
            "version": "1.1",
            "params": params,
        }

        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = self.token

        response = self._http.post(url, json=request, headers=headers)
        response.raise_for_status()

        data = response.json()
        if "error" in data and data["error"]:
            err = data["error"]
            raise Exception(f"RPC Error {err.get('code')}: {err.get('message')}")

        return data.get("result")


def _load_token() -> str:
    """Load token from environment or file."""
    # Check environment
    token = os.environ.get("BVBRC_TOKEN") or os.environ.get("P3_AUTH_TOKEN")
    if token:
        return token.strip()

    # Check token files
    home = Path.home()
    for filename in [".bvbrc_token", ".patric_token", ".p3_token"]:
        token_path = home / filename
        if token_path.exists():
            try:
                return token_path.read_text().strip()
            except Exception:
                continue

    return ""


def _parse_workspace_object(tuple_data: list[Any]) -> WorkspaceObject:
    """Parse workspace object tuple."""
    return WorkspaceObject(
        path=str(tuple_data[0]) if len(tuple_data) > 0 else "",
        type=str(tuple_data[1]) if len(tuple_data) > 1 else "",
        owner=str(tuple_data[2]) if len(tuple_data) > 2 else "",
        creation_time=str(tuple_data[3]) if len(tuple_data) > 3 else "",
        id=str(tuple_data[4]) if len(tuple_data) > 4 else "",
        size=int(tuple_data[6]) if len(tuple_data) > 6 and tuple_data[6] else 0,
        user_metadata=tuple_data[7] if len(tuple_data) > 7 else {},
        auto_metadata=tuple_data[8] if len(tuple_data) > 8 else {},
        shock_ref=str(tuple_data[9]) if len(tuple_data) > 9 and tuple_data[9] else None,
        data=str(tuple_data[11]) if len(tuple_data) > 11 and tuple_data[11] else None,
    )
