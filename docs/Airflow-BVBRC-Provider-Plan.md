# Apache Airflow BV-BRC Provider Plan

> **Status**: Planned (for later implementation)
> **Date**: 2026-02-09
> **Purpose**: Custom Airflow provider package to submit and manage BV-BRC bioinformatics jobs from Airflow DAGs.

---

## 1. Overview

Create an `apache-airflow-providers-bvbrc` Python package that enables Airflow users to submit, monitor, and manage BV-BRC analysis jobs directly from Airflow DAGs. This provides an immediate, low-effort integration path using the mature Airflow ecosystem while GoWe is being developed.

### Why This Matters

- **Immediate value**: Airflow is already deployed in many research computing environments.
- **Proven infrastructure**: Scheduling, retry, monitoring, and UI come for free.
- **Bridge strategy**: Serves users now; GoWe replaces it later for Go-native workflows.

---

## 2. Components

### 2.1 BVBRCHook (`hooks/bvbrc.py`)

Manages authentication and JSON-RPC communication with BV-BRC services.

```python
class BVBRCHook(BaseHook):
    """
    Hook for BV-BRC JSON-RPC API communication.

    Connection parameters (Airflow connection UI):
      - Host: https://p3.theseed.org/services/
      - Login: BV-BRC username
      - Password: BV-BRC password
      - Extra: {"app_service_url": "...", "workspace_url": "..."}
    """

    conn_name_attr = "bvbrc_conn_id"
    default_conn_name = "bvbrc_default"
    conn_type = "bvbrc"
    hook_name = "BV-BRC"

    def __init__(self, bvbrc_conn_id="bvbrc_default"):
        super().__init__()
        self.bvbrc_conn_id = bvbrc_conn_id
        self._token = None
        self._token_expiry = None

    def authenticate(self) -> str:
        """Authenticate and return bearer token."""
        # POST to https://user.patricbrc.org/authenticate
        # Cache token until expiry

    def rpc_call(self, service_url: str, method: str, params: list) -> dict:
        """Execute a JSON-RPC 1.1 call."""
        # POST with Content-Type: application/json
        # Include Authorization: Bearer <token>
        # Handle errors, retries

    def submit_job(self, app_id: str, params: dict, workspace: str) -> str:
        """Submit a job via AppService.start_app. Returns task_id."""

    def query_task(self, task_id: str) -> dict:
        """Query task status via AppService.query_tasks."""

    def list_apps(self) -> list:
        """List available apps via AppService.enumerate_apps."""

    def workspace_ls(self, path: str) -> list:
        """List workspace contents via Workspace.ls."""

    def workspace_get(self, paths: list) -> list:
        """Get workspace objects via Workspace.get."""
```

### 2.2 BVBRCOperator (`operators/bvbrc.py`)

Base operator for submitting any BV-BRC app job.

```python
class BVBRCOperator(BaseOperator):
    """
    Submit a BV-BRC analysis job and wait for completion.

    :param app_id: BV-BRC app name (e.g., "GenomeAssembly2")
    :param job_params: Dict of app-specific parameters
    :param output_path: Workspace path for results
    :param bvbrc_conn_id: Airflow connection ID
    :param poll_interval: Seconds between status checks (default 60)
    :param timeout: Max seconds to wait (default 86400 = 24h)
    """

    template_fields = ("app_id", "job_params", "output_path")

    def __init__(
        self,
        app_id: str,
        job_params: dict,
        output_path: str,
        bvbrc_conn_id: str = "bvbrc_default",
        poll_interval: int = 60,
        timeout: int = 86400,
        **kwargs,
    ):
        super().__init__(**kwargs)
        self.app_id = app_id
        self.job_params = job_params
        self.output_path = output_path
        self.bvbrc_conn_id = bvbrc_conn_id
        self.poll_interval = poll_interval
        self.timeout = timeout

    def execute(self, context):
        hook = BVBRCHook(bvbrc_conn_id=self.bvbrc_conn_id)

        # 1. Submit job
        task_id = hook.submit_job(
            app_id=self.app_id,
            params=self.job_params,
            workspace=self.output_path,
        )
        self.log.info(f"Submitted BV-BRC job: {task_id}")

        # 2. Push task_id to XCom for downstream tasks
        context["ti"].xcom_push(key="bvbrc_task_id", value=task_id)

        # 3. Poll until completion
        start = time.time()
        while True:
            status = hook.query_task(task_id)
            state = status.get("status")
            self.log.info(f"Job {task_id}: {state}")

            if state == "completed":
                return status
            elif state in ("failed", "deleted"):
                raise AirflowException(f"BV-BRC job {task_id} {state}")
            elif time.time() - start > self.timeout:
                raise AirflowException(f"BV-BRC job {task_id} timed out")

            time.sleep(self.poll_interval)
```

### 2.3 Typed App Operators

Convenience operators with validated parameters for common BV-BRC apps:

```python
class BVBRCGenomeAssemblyOperator(BVBRCOperator):
    """Submit a genome assembly job with validated parameters."""

    def __init__(
        self,
        paired_end_reads: list = None,    # [{"read1": path, "read2": path}]
        single_end_reads: list = None,
        srr_ids: list = None,
        recipe: str = "auto",             # auto | unicycler | spades | ...
        trim: bool = True,
        output_path: str = None,
        output_file: str = "assembly",
        **kwargs,
    ):
        job_params = {
            "recipe": recipe,
            "trim": "true" if trim else "false",
            "output_path": output_path,
            "output_file": output_file,
        }
        if paired_end_reads:
            job_params["paired_end_libs"] = paired_end_reads
        if single_end_reads:
            job_params["single_end_libs"] = single_end_reads
        if srr_ids:
            job_params["srr_ids"] = srr_ids

        super().__init__(
            app_id="GenomeAssembly2",
            job_params=job_params,
            output_path=output_path,
            **kwargs,
        )


class BVBRCGenomeAnnotationOperator(BVBRCOperator):
    """Submit a genome annotation job."""

    def __init__(
        self,
        contigs: str,                      # Workspace path to contigs
        scientific_name: str,
        taxonomy_id: int,
        genetic_code: int = 11,
        domain: str = "Bacteria",
        output_path: str = None,
        output_file: str = "annotation",
        **kwargs,
    ):
        job_params = {
            "contigs": contigs,
            "scientific_name": scientific_name,
            "taxonomy_id": taxonomy_id,
            "code": genetic_code,
            "domain": domain,
            "output_path": output_path,
            "output_file": output_file,
        }
        super().__init__(
            app_id="GenomeAnnotation",
            job_params=job_params,
            output_path=output_path,
            **kwargs,
        )


class BVBRCComprehensiveGenomeAnalysisOperator(BVBRCOperator):
    """Submit a CGA job (assembly + annotation pipeline)."""
    pass  # Similar pattern


class BVBRCTaxonomicClassificationOperator(BVBRCOperator):
    """Submit a taxonomic classification job."""
    pass  # Similar pattern
```

### 2.4 BVBRCSensor (`sensors/bvbrc.py`)

For DAGs that submit jobs externally and only need to wait:

```python
class BVBRCJobSensor(BaseSensorOperator):
    """
    Wait for a BV-BRC job to reach a terminal state.

    :param task_id: BV-BRC task ID to monitor
    :param bvbrc_conn_id: Airflow connection ID
    """

    template_fields = ("bvbrc_task_id",)

    def __init__(self, bvbrc_task_id: str, bvbrc_conn_id="bvbrc_default", **kwargs):
        super().__init__(**kwargs)
        self.bvbrc_task_id = bvbrc_task_id
        self.bvbrc_conn_id = bvbrc_conn_id

    def poke(self, context) -> bool:
        hook = BVBRCHook(bvbrc_conn_id=self.bvbrc_conn_id)
        status = hook.query_task(self.bvbrc_task_id)
        state = status.get("status")

        if state == "completed":
            return True
        elif state in ("failed", "deleted"):
            raise AirflowException(f"BV-BRC job {self.bvbrc_task_id} {state}")
        return False
```

---

## 3. Example DAG

```python
from datetime import datetime
from airflow import DAG
from airflow_providers_bvbrc.operators.bvbrc import (
    BVBRCGenomeAssemblyOperator,
    BVBRCGenomeAnnotationOperator,
)

with DAG(
    dag_id="bvbrc_assembly_annotation_pipeline",
    start_date=datetime(2026, 1, 1),
    schedule_interval=None,  # Manual trigger
    catchup=False,
    tags=["bvbrc", "genomics"],
) as dag:

    assemble = BVBRCGenomeAssemblyOperator(
        task_id="assemble_genome",
        paired_end_reads=[{
            "read1": "/user@bvbrc/home/reads/sample1_R1.fastq.gz",
            "read2": "/user@bvbrc/home/reads/sample1_R2.fastq.gz",
        }],
        recipe="auto",
        trim=True,
        output_path="/user@bvbrc/home/assemblies/sample1",
        output_file="sample1_assembly",
        poll_interval=120,
    )

    annotate = BVBRCGenomeAnnotationOperator(
        task_id="annotate_genome",
        contigs="{{ ti.xcom_pull(task_ids='assemble_genome')['output_path'] }}/sample1_assembly.contigs.fasta",
        scientific_name="Escherichia coli",
        taxonomy_id=562,
        output_path="/user@bvbrc/home/annotations/sample1",
        output_file="sample1_annotation",
        poll_interval=120,
    )

    assemble >> annotate
```

---

## 4. Package Structure

```
airflow-providers-bvbrc/
├── pyproject.toml
├── README.md
├── airflow_providers_bvbrc/
│   ├── __init__.py
│   ├── hooks/
│   │   ├── __init__.py
│   │   └── bvbrc.py              # BVBRCHook
│   ├── operators/
│   │   ├── __init__.py
│   │   ├── bvbrc.py              # BVBRCOperator (base)
│   │   ├── assembly.py           # BVBRCGenomeAssemblyOperator
│   │   ├── annotation.py         # BVBRCGenomeAnnotationOperator
│   │   └── cga.py                # BVBRCComprehensiveGenomeAnalysisOperator
│   ├── sensors/
│   │   ├── __init__.py
│   │   └── bvbrc.py              # BVBRCJobSensor
│   └── example_dags/
│       ├── assembly_annotation.py
│       └── batch_assembly.py
└── tests/
    ├── hooks/
    │   └── test_bvbrc.py
    ├── operators/
    │   └── test_bvbrc.py
    └── sensors/
        └── test_bvbrc.py
```

---

## 5. Implementation Roadmap

| Phase | Deliverable | Effort |
|-------|-------------|--------|
| **1** | BVBRCHook (auth + JSON-RPC client) | 1-2 days |
| **2** | BVBRCOperator (base submit + poll) | 1 day |
| **3** | BVBRCSensor | 0.5 day |
| **4** | Typed app operators (Assembly, Annotation, CGA) | 1-2 days |
| **5** | Example DAGs + documentation | 1 day |
| **6** | Tests (mock BV-BRC API) | 1-2 days |
| **7** | PyPI packaging + Airflow provider metadata | 0.5 day |

**Total estimated effort**: ~1 week

---

## 6. Airflow Connection Configuration

Users configure a BV-BRC connection in Airflow UI or via environment variable:

```bash
# Environment variable format
export AIRFLOW_CONN_BVBRC_DEFAULT='bvbrc://username:password@p3.theseed.org:443/services?extra__bvbrc__workspace_url=https://p3.theseed.org/services/Workspace'
```

Or via Airflow UI:
- **Connection ID**: `bvbrc_default`
- **Connection Type**: `BV-BRC`
- **Host**: `https://p3.theseed.org/services/`
- **Login**: BV-BRC username
- **Password**: BV-BRC password
- **Extra** (JSON):
  ```json
  {
    "app_service_url": "https://p3.theseed.org/services/app_service",
    "workspace_url": "https://p3.theseed.org/services/Workspace",
    "auth_url": "https://user.patricbrc.org/authenticate",
    "shock_url": "https://p3.theseed.org/services/shock_api"
  }
  ```

---

## 7. Relationship to GoWe

This Airflow provider is a **tactical solution** while GoWe is under development:

| Aspect | Airflow Provider | GoWe |
|--------|-----------------|------|
| **Timeline** | Now (~1 week) | Medium-term |
| **Language** | Python | Go |
| **Dependency** | Requires Airflow infrastructure | Standalone binary |
| **Workflow format** | Python DAGs | CWL / GoWe JSON |
| **Target users** | Teams already using Airflow | All BV-BRC users |
| **Maintenance** | Minimal (thin wrapper) | Full engine development |

The Airflow provider validates the BV-BRC integration patterns (auth, job submission, status polling, workspace access) that GoWe will later implement natively in Go.
