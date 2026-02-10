# Workflow Engines Comparison for GoWe Design

> **Purpose**: Research and compare five workflow engines to inform the design of GoWe -- a Go-based workflow engine that creates formal workflow definitions, submits them for validation/management/monitoring/scheduling, and integrates with the BV-BRC job submission system (JSON-RPC API for bioinformatics jobs).
>
> **Date**: 2026-02-09
> **Author**: GoWe Architecture Research

---

## Table of Contents

1. [Individual Summaries](#1-individual-summaries)
   - [1.1 Nextflow](#11-nextflow)
   - [1.2 Snakemake](#12-snakemake)
   - [1.3 Apache Airflow](#13-apache-airflow)
   - [1.4 Parsl](#14-parsl)
   - [1.5 AWE](#15-awe-analysis-of-workflow-engine)
2. [Comparative Analysis Table](#2-comparative-analysis-table)
3. [Common Concepts](#3-common-concepts)
4. [Differentiating Concepts](#4-differentiating-concepts)
5. [Ranking for BV-BRC Integration](#5-ranking-for-bv-brc-integration)
6. [Recommendations for GoWe](#6-recommendations-for-gowe)

---

## 1. Individual Summaries

### 1.1 Nextflow

**Website**: https://www.nextflow.io/
**Repository**: https://github.com/nextflow-io/nextflow
**Implementation Language**: Groovy (JVM)
**License**: Apache 2.0

#### Core Design Philosophy

Nextflow is built on the **dataflow programming model**, where the flow of data between processes determines execution order rather than explicit control flow. This reactive paradigm means that processes execute as soon as their input data becomes available, enabling implicit parallelism without explicit synchronization. The philosophy emphasizes:

- **Separation of concerns**: Workflow logic is separated from execution details (where and how processes run).
- **Reproducibility**: Native container support (Docker, Singularity, Conda) ensures identical execution environments.
- **Portability**: The same pipeline can run on a laptop, HPC cluster, or cloud without modification to the workflow logic.
- **Continuous streaming**: Data flows through channels in a streaming fashion, avoiding batch bottlenecks.

#### Workflow Definition Format/Language

Nextflow uses a custom **Domain-Specific Language (DSL)** built on Groovy. The current standard is **DSL2**, which introduced modular workflow composition.

```groovy
// DSL2 Example
process FASTQC {
    container 'biocontainers/fastqc:0.11.9'

    input:
    path reads

    output:
    path "*.html", emit: reports

    script:
    """
    fastqc ${reads}
    """
}

process MULTIQC {
    input:
    path reports

    output:
    path "multiqc_report.html"

    script:
    """
    multiqc .
    """
}

workflow {
    reads_ch = Channel.fromFilePairs("data/*_{1,2}.fastq.gz")
    FASTQC(reads_ch)
    MULTIQC(FASTQC.out.reports.collect())
}
```

Key DSL concepts:
- **Processes**: Atomic units of computation with defined inputs, outputs, scripts, and directives.
- **Channels**: Asynchronous FIFO queues that connect processes and carry data.
- **Operators**: Transformations applied to channels (map, filter, collect, merge, etc.).
- **Workflows**: Named compositions of processes and sub-workflows (DSL2).
- **Modules**: Reusable process definitions that can be imported across workflows.

#### Execution Model

- **Dataflow-driven**: Each process instance is triggered when all required input channels have data available.
- **Implicit parallelism**: Multiple instances of the same process run concurrently across different input items.
- **Channel semantics**: Channels are consumed (each item read once) unless explicitly shared.
- **Work directory**: Each task runs in an isolated work directory with staged inputs and captured outputs.
- **Caching/resume**: Results are cached by input hash; failed pipelines can resume from the last successful step.

#### Scheduling Approach

Nextflow supports multiple **executors** that abstract the scheduling backend:
- **Local**: Thread pool on the local machine.
- **SLURM, PBS, SGE, LSF**: HPC cluster batch schedulers.
- **AWS Batch, Google Cloud Batch, Azure Batch**: Cloud-native batch execution.
- **Kubernetes**: Container orchestration.
- **Nextflow Tower (Seqera Platform)**: Managed execution with centralized monitoring.

Resource requirements (CPUs, memory, time) are specified per-process via directives and passed to the executor.

#### Data/File Management

- Files are staged into task work directories via symlinks or copies.
- Output files are published to user-specified directories via `publishDir`.
- Supports S3, GCS, Azure Blob as native file paths.
- Channel factories create channels from files, file pairs, and directory listings.

#### Monitoring Capabilities

- **Nextflow Tower / Seqera Platform**: Web-based monitoring with real-time progress, resource utilization, and cost tracking.
- **Execution reports**: HTML reports with timeline, resource usage, and task-level details.
- **Trace files**: Tab-separated logs of every task with metrics.
- **Timeline visualization**: Gantt-chart-like view of task execution.
- **Log files**: Detailed `.nextflow.log` with full execution trace.

#### API/Programmatic Access

- **CLI-driven**: Primary interface is the `nextflow run` command.
- **Seqera Platform API**: REST API for launching, monitoring, and managing pipelines.
- **nf-core tools**: Python CLI for managing community pipelines.
- No built-in REST API in the core engine itself.

#### Container Support

- **Docker**: Native support, per-process container specification.
- **Singularity/Apptainer**: First-class support for HPC environments.
- **Conda**: Environment management as an alternative to containers.
- **Podman, Charliecloud, Sarus**: Additional container runtimes.
- **Wave**: Dynamic container provisioning (Seqera).

#### Extensibility/Plugin Model

- **Plugin system**: Extensible via Nextflow plugins (e.g., nf-amazon, nf-google, nf-azure).
- **Custom executors**: New execution backends can be added as plugins.
- **nf-core**: Community-curated pipeline repository with standardized patterns.
- **Modules**: Shared process definitions via nf-core modules.

---

### 1.2 Snakemake

**Website**: https://snakemake.github.io/
**Repository**: https://github.com/snakemake/snakemake
**Implementation Language**: Python
**License**: MIT

#### Core Design Philosophy

Snakemake follows a **rule-based, declarative approach** inspired by GNU Make. The philosophy centers on:

- **Target-driven execution**: Users specify desired output files, and Snakemake determines the chain of rules needed to produce them by working backward from targets.
- **Implicit DAG construction**: The dependency graph is inferred from input/output file patterns rather than being explicitly defined.
- **Pythonic integration**: Rules can embed Python code directly, leveraging the entire Python ecosystem.
- **Reproducibility**: Built-in support for Conda environments, containers, and provenance tracking.
- **Scalability**: Same workflow runs locally or on clusters/clouds with minimal changes.

#### Workflow Definition Format/Language

Snakemake uses a **Python-based DSL** in files called `Snakefile`:

```python
# Snakefile Example
configfile: "config.yaml"

rule all:
    input:
        "results/multiqc_report.html"

rule fastqc:
    input:
        "data/{sample}.fastq.gz"
    output:
        html="qc/{sample}_fastqc.html",
        zip="qc/{sample}_fastqc.zip"
    conda:
        "envs/fastqc.yaml"
    threads: 2
    shell:
        "fastqc --threads {threads} {input} -o qc/"

rule multiqc:
    input:
        expand("qc/{sample}_fastqc.html", sample=config["samples"])
    output:
        "results/multiqc_report.html"
    shell:
        "multiqc qc/ -o results/"
```

Key concepts:
- **Rules**: Define transformations from input files to output files.
- **Wildcards**: `{sample}` patterns that generalize rules across datasets.
- **expand()**: Generates lists of filenames from patterns and variable lists.
- **Config files**: YAML/JSON configuration that parameterizes workflows.
- **run/shell/script**: Multiple ways to specify rule execution (inline Python, shell commands, external scripts).

#### Execution Model

- **DAG construction**: Snakemake resolves the requested targets backward through rules to build a directed acyclic graph.
- **File-based dependencies**: Edges in the DAG are defined by input/output file relationships.
- **Wildcard resolution**: Wildcards in file patterns are resolved to create concrete job instances.
- **Conditional execution**: Jobs only run if outputs are missing or older than inputs (make-like behavior).
- **Checkpoints**: Rules whose output structure is not known until runtime, allowing dynamic DAG modification.

#### Scheduling Approach

- **Local**: Multi-threaded execution with a thread pool.
- **Cluster execution**: Generic `--cluster` flag for submitting jobs to any batch system via custom submission commands.
- **SLURM, PBS, LSF, SGE**: Dedicated executor plugins with native integration.
- **Kubernetes**: Container-based execution on Kubernetes clusters.
- **Google Cloud Life Sciences, AWS, Azure**: Cloud execution backends.
- **Snakemake profiles**: Reusable configuration bundles for specific execution environments.

Resource management:
- `threads`, `resources` (custom resource types), and `priority` directives per rule.
- Global resource constraints limit concurrent usage.

#### Data/File Management

- **File-centric**: The entire model revolves around input and output files.
- **Remote files**: Support for S3, GCS, HTTP, FTP, SFTP, Dropbox, and more via remote providers.
- **Protected/temporary outputs**: Outputs can be marked as protected (read-only) or temporary (auto-deleted).
- **Shadow directories**: Isolated execution directories to prevent interference between jobs.
- **Benchmarking**: Built-in resource usage benchmarking per rule.

#### Monitoring Capabilities

- **Panoptes**: Web-based real-time monitoring dashboard for Snakemake workflows.
- **HTML reports**: `snakemake --report` generates comprehensive HTML reports with provenance.
- **DAG visualization**: `snakemake --dag | dot -Tpdf > dag.pdf` for graphical DAG output.
- **Logging**: Per-rule log file specification via `log` directive.
- **Workflow catalog**: Standardized workflow publishing at snakemake.github.io/snakemake-workflow-catalog.

#### API/Programmatic Access

- **CLI-driven**: Primary interface is the `snakemake` command.
- **Python API**: `snakemake.snakemake()` function for programmatic invocation from Python.
- **No built-in REST API**: Server mode is not a core feature (though Panoptes provides some).
- **Wrappers**: Community-maintained tool wrappers for common bioinformatics tools.

#### Container Support

- **Singularity/Apptainer**: Per-rule container specification via `singularity` directive.
- **Docker**: Supported via `--use-singularity` or direct Docker execution.
- **Conda**: First-class support via `conda` directive pointing to environment YAML files.
- **Environment Modules**: Support for HPC module systems.

#### Extensibility/Plugin Model

- **Executor plugins**: Modular backends for different execution environments (since Snakemake 8).
- **Storage plugins**: Pluggable remote storage backends.
- **Wrappers repository**: Community wrappers at https://snakemake-wrappers.readthedocs.io/.
- **Workflow catalog**: Standardized, reusable workflow discovery.

---

### 1.3 Apache Airflow

**Website**: https://airflow.apache.org/
**Repository**: https://github.com/apache/airflow
**Implementation Language**: Python
**License**: Apache 2.0

#### Core Design Philosophy

Apache Airflow is designed as a **platform for programmatically authoring, scheduling, and monitoring workflows**. Its philosophy emphasizes:

- **Workflows as code**: DAGs are defined in Python, enabling dynamic generation, version control, and testing.
- **Orchestration, not execution**: Airflow orchestrates tasks that run on external systems rather than executing heavy computation itself.
- **Scheduling focus**: Unlike Nextflow/Snakemake which are pipeline-runners, Airflow is a full scheduling platform for recurring and triggered workflows.
- **Extensibility**: A rich operator and hook ecosystem allows integration with virtually any external system.
- **Enterprise-grade**: Built for production operations with authentication, RBAC, audit logging, and SLA management.

#### Workflow Definition Format/Language

DAGs are defined in Python, stored as `.py` files in a configurable `dags/` folder:

```python
from airflow import DAG
from airflow.operators.bash import BashOperator
from airflow.operators.python import PythonOperator
from airflow.providers.http.operators.http import HttpOperator
from datetime import datetime, timedelta

default_args = {
    'owner': 'bioinformatics',
    'retries': 3,
    'retry_delay': timedelta(minutes=5),
}

with DAG(
    dag_id='bvbrc_analysis_pipeline',
    default_args=default_args,
    description='Run bioinformatics analysis via BV-BRC',
    schedule_interval='@daily',
    start_date=datetime(2024, 1, 1),
    catchup=False,
    tags=['bioinformatics', 'bvbrc'],
) as dag:

    submit_job = HttpOperator(
        task_id='submit_assembly_job',
        http_conn_id='bvbrc_api',
        endpoint='/rpc',
        method='POST',
        data='{"jsonrpc":"2.0","method":"submit_job",...}',
    )

    check_status = PythonOperator(
        task_id='check_job_status',
        python_callable=poll_job_status,
    )

    download_results = BashOperator(
        task_id='download_results',
        bash_command='wget ...',
    )

    submit_job >> check_status >> download_results
```

Key concepts:
- **DAG**: A directed acyclic graph of tasks with dependencies.
- **Tasks**: Instances of operators that define a unit of work.
- **Operators**: Templates for tasks (BashOperator, PythonOperator, HttpOperator, etc.).
- **Hooks**: Interfaces to external platforms (databases, APIs, cloud services).
- **Sensors**: Special operators that wait for an external condition.
- **XComs**: Cross-communication mechanism for passing small data between tasks.
- **TaskFlow API**: Decorator-based syntax (`@task`) for simpler Python task definitions.
- **Connections/Variables**: Centralized credential and configuration management.

#### Execution Model

Airflow has a multi-component architecture:

1. **Scheduler**: Continuously parses DAG files, creates DAG runs based on schedules/triggers, and submits task instances to the executor.
2. **Executor**: Determines how tasks are run:
   - **SequentialExecutor**: Single-threaded, for development only.
   - **LocalExecutor**: Multi-process on a single machine.
   - **CeleryExecutor**: Distributed execution via Celery workers with a message broker (Redis/RabbitMQ).
   - **KubernetesExecutor**: Each task runs as a Kubernetes pod.
   - **CeleryKubernetesExecutor**: Hybrid approach.
   - **DaskExecutor**: Distributed execution via Dask.
3. **Web Server**: Flask-based UI for monitoring, triggering, and managing DAGs.
4. **Metadata Database**: PostgreSQL/MySQL storing all state (DAG runs, task instances, connections, variables).
5. **Workers**: Process task instances (in Celery/Kubernetes modes).
6. **Triggerer**: Handles deferred tasks that wait for external events asynchronously.

#### Scheduling Approach

- **Time-based scheduling**: Cron expressions or preset schedules (`@daily`, `@hourly`).
- **Dataset-triggered**: DAGs can trigger when upstream datasets are updated (Airflow 2.4+).
- **Manual triggering**: Via UI or API.
- **External triggering**: Via REST API calls.
- **Catchup**: Optionally backfill historical runs.
- **Dependencies**: Cross-DAG dependencies via `ExternalTaskSensor` or `TriggerDagRunOperator`.
- **Pools**: Limit concurrent tasks across DAGs for resource management.
- **Priority weights**: Task-level priority for scheduling order.

#### Data/File Management

- Airflow is **not designed for large data transfer** between tasks.
- **XComs**: Small metadata/results passing (stored in the database, default 48KB limit).
- **External storage**: Tasks typically read/write to external systems (S3, databases, etc.).
- **Datasets**: Logical representations of data produced by tasks (for scheduling triggers).
- No built-in file staging; tasks manage their own I/O.

#### Monitoring Capabilities

- **Web UI**: Rich web interface with:
  - DAG visualization (graph view, tree view, Gantt chart).
  - Task instance logs (streamed in real-time).
  - Run history and statistics.
  - SLA miss tracking.
  - Trigger and configuration management.
- **REST API**: Full CRUD operations on DAGs, DAG runs, task instances, connections, variables, pools.
- **Metrics**: StatsD/Prometheus integration for operational metrics.
- **Alerting**: Email, Slack, PagerDuty notifications on task failure/SLA miss.
- **Audit logs**: Track who did what and when.

#### API/Programmatic Access

- **REST API** (stable since Airflow 2.0): Comprehensive API for:
  - Listing and managing DAGs.
  - Triggering DAG runs with configuration.
  - Monitoring task instance states.
  - Managing connections, variables, pools.
  - Retrieving logs.
- **CLI**: `airflow` command-line tool for all operations.
- **Python SDK**: Programmatic DAG generation.

#### Container Support

- **KubernetesExecutor**: Each task runs in its own pod with a configurable container image.
- **DockerOperator**: Run individual tasks in Docker containers.
- **KubernetesPodOperator**: Explicit Kubernetes pod execution with full pod spec control.
- **Helm chart**: Official Helm chart for deploying Airflow on Kubernetes.

#### Extensibility/Plugin Model

- **Provider packages**: 70+ official provider packages (AWS, GCP, Azure, Databricks, dbt, etc.).
- **Custom operators**: Extend `BaseOperator` to create new task types.
- **Custom hooks**: Extend `BaseHook` for new system integrations.
- **Custom sensors**: Create waiters for any external condition.
- **Plugin system**: Full plugin API for adding UI views, operators, hooks, macros, etc.
- **Connections**: Pluggable authentication for external systems.

---

### 1.4 Parsl

**Website**: https://parsl-project.org/
**Repository**: https://github.com/Parsl/parsl
**Implementation Language**: Python
**License**: Apache 2.0

#### Core Design Philosophy

Parsl (Parallel Scripting Library) is designed for **Python-native parallel programming** with a focus on:

- **Minimal intrusion**: Add parallelism to existing Python code with decorators rather than learning a new DSL.
- **Implicit parallelism**: Dependencies between tasks are automatically resolved via Python futures.
- **Separation of logic and execution**: Application logic is decoupled from where/how tasks execute.
- **Multi-site execution**: A single script can dispatch tasks across laptops, clusters, clouds, and supercomputers simultaneously.
- **Scientific computing focus**: Designed for computational science workloads with complex resource requirements.

#### Workflow Definition Format/Language

Parsl uses **Python decorators** to define parallel tasks -- no external DSL or configuration language:

```python
import parsl
from parsl import python_app, bash_app
from parsl.config import Config
from parsl.executors import HighThroughputExecutor
from parsl.providers import SlurmProvider

# Configuration defines WHERE tasks run
config = Config(
    executors=[
        HighThroughputExecutor(
            label='slurm_htex',
            provider=SlurmProvider(
                partition='compute',
                nodes_per_block=1,
                max_blocks=10,
                walltime='01:00:00',
            ),
        )
    ]
)
parsl.load(config)

@bash_app
def fastqc(input_file, stdout='fastqc.stdout'):
    return f'fastqc {input_file}'

@python_app
def parse_results(qc_output):
    import json
    with open(qc_output.filepath) as f:
        return json.load(f)

@python_app
def aggregate(results=[]):
    return {r['sample']: r['score'] for r in results}

# Implicit parallelism via futures
futures = [fastqc(f) for f in input_files]
parsed = [parse_results(f) for f in futures]
summary = aggregate(results=parsed)

# Blocks until complete
print(summary.result())
```

Key concepts:
- **Apps**: Decorated functions that become parallel tasks.
  - `@python_app`: Pure Python functions serialized and shipped to workers.
  - `@bash_app`: Shell commands executed on workers.
  - `@join_app`: Apps that return other app futures (dynamic workflows).
- **Futures**: App invocations return `AppFuture` objects; passing a future as input to another app creates an implicit dependency.
- **DataFutures**: Represent files produced by apps, enabling file-based dependencies.
- **Configuration**: Separate `Config` object defines executors, providers, and resource allocation.

#### Execution Model

- **DataFlow Kernel (DFK)**: Central component that tracks task dependencies (via futures) and dispatches ready tasks to executors.
- **Executors**: Determine how tasks are parallelized:
  - **HighThroughputExecutor (HTEX)**: Pilot job model with managers and workers; highly scalable.
  - **WorkQueueExecutor**: Integration with Work Queue for distributed task management.
  - **ThreadPoolExecutor**: Local threading for lightweight tasks.
  - **FluxExecutor**: Integration with Flux resource manager.
- **Providers**: Acquire compute resources from different backends:
  - **LocalProvider**: Local machine.
  - **SlurmProvider, PBSProvider, CobaltProvider, LSFProvider, GridEngineProvider**: HPC schedulers.
  - **AWSProvider, GoogleCloudProvider**: Cloud compute.
  - **KubernetesProvider**: Container orchestration.
- **Launchers**: Determine how workers are launched within allocated resources (srun, mpiexec, etc.).

#### Scheduling Approach

- **Automatic dependency resolution**: The DFK monitors futures and submits tasks when dependencies are satisfied.
- **Elastic scaling**: Providers auto-scale compute blocks up and down based on task queue depth.
- **Resource specifications**: Tasks can specify CPU, memory, GPU, and custom resource requirements.
- **Multi-executor**: Different task types can be routed to different executors based on resource needs.
- **Retries**: Configurable retry count per app with retry handlers.

#### Data/File Management

- **File objects**: `parsl.data_provider.files.File` wraps file references with support for local, HTTP, FTP, and Globus paths.
- **Globus integration**: First-class support for Globus data transfer between sites.
- **Staging providers**: Pluggable data staging for moving files to/from execution sites.
- **DataFutures**: Track file outputs and stage them automatically when needed by downstream tasks.

#### Monitoring Capabilities

- **Monitoring Hub**: Built-in monitoring database (SQLite) that records task states, resource usage, and workflow progress.
- **Visualization**: Web-based dashboard for real-time workflow monitoring.
- **Logging**: Configurable logging with per-task log files.
- **Usage tracking**: Task-level resource consumption metrics.

#### API/Programmatic Access

- **Python-native**: The API is Python itself -- no separate API layer.
- **No REST API**: Being a library, there is no server component with a REST API by default.
- **Monitoring database**: Can be queried programmatically for workflow state.
- **Programmatic configuration**: Full control via Python `Config` objects.

#### Container Support

- **Singularity**: Supported via executor configuration.
- **Docker**: Tasks can be configured to run in Docker containers.
- **Shifter**: Support for NERSC's container runtime.
- Container specification is at the executor level rather than per-task.

#### Extensibility/Plugin Model

- **Custom executors**: Implement the executor interface for new backends.
- **Custom providers**: Add new resource providers.
- **Custom launchers**: Define how workers start on new systems.
- **Custom data staging**: Pluggable staging providers.
- **Decorator-based**: New app types can be defined.

---

### 1.5 AWE (Analysis of Workflow Engine)

**Website**: https://github.com/MG-RAST/AWE
**Implementation Language**: Go
**License**: BSD 3-Clause

> **Note**: AWE is particularly relevant to GoWe as it is the only Go-based workflow engine in this comparison and was designed for bioinformatics workloads at MG-RAST/Argonne National Laboratory.

#### Core Design Philosophy

AWE is a **lightweight, scalable workflow engine** designed for high-throughput bioinformatics data analysis. Its philosophy emphasizes:

- **Client-server architecture**: A central server manages jobs while distributed clients (workers) pull and execute tasks.
- **Work-queue model**: Tasks are distributed to workers via a pull-based work queue, enabling elastic scaling.
- **RESTful API-first**: All interactions (submission, monitoring, management) happen via a REST API.
- **Data-aware scheduling**: Integration with Shock (a data management system) for efficient data staging.
- **Simplicity**: Minimalist design focusing on reliable task distribution rather than complex workflow semantics.
- **Go-native**: Leverages Go's concurrency model (goroutines, channels) for efficient server implementation.

#### Workflow Definition Format/Language

AWE uses **JSON-based job documents** to define workflows:

```json
{
    "info": {
        "pipeline": "mgrast-pipeline",
        "name": "my-metagenome-analysis",
        "project": "metagenomics-study-1",
        "user": "researcher",
        "clientgroups": "default",
        "noretry": false
    },
    "tasks": [
        {
            "taskid": "0",
            "cmd": {
                "name": "awe_preprocess.pl",
                "args": "-input @input.fasta -output preprocessed.fasta",
                "description": "Preprocess raw sequences",
                "environ": {
                    "public": {"PERL5LIB": "/usr/local/lib"}
                }
            },
            "dependsOn": [],
            "inputs": {
                "input.fasta": {
                    "host": "http://shock-server:7445",
                    "node": "abc123-def456"
                }
            },
            "outputs": {
                "preprocessed.fasta": {
                    "host": "http://shock-server:7445"
                }
            },
            "partinfo": {
                "input": "input.fasta",
                "output": "preprocessed.fasta"
            },
            "totalwork": 1
        },
        {
            "taskid": "1",
            "cmd": {
                "name": "awe_annotate.pl",
                "args": "-input @preprocessed.fasta -output annotated.tab"
            },
            "dependsOn": ["0"],
            "inputs": {
                "preprocessed.fasta": {
                    "host": "http://shock-server:7445",
                    "origin": "0"
                }
            },
            "outputs": {
                "annotated.tab": {
                    "host": "http://shock-server:7445"
                }
            },
            "totalwork": 1
        }
    ]
}
```

Key concepts:
- **Jobs**: Top-level workflow unit containing metadata and an ordered list of tasks.
- **Tasks**: Individual compute steps with commands, inputs, outputs, and dependencies.
- **Dependencies**: Explicit `dependsOn` array referencing task IDs.
- **Inputs/Outputs**: Files with Shock node references for data management.
- **Partinfo**: Specification for splitting tasks across data partitions (scatter/gather).
- **Client groups**: Label-based routing to specific worker pools.

#### Execution Model

AWE uses a **client-server model with work stealing**:

1. **AWE Server**:
   - Receives job submissions via REST API.
   - Parses job documents and creates a task queue.
   - Manages task state machine: `init -> queued -> in-progress -> completed/failed`.
   - Tracks dependencies and enqueues tasks when prerequisites are met.
   - Serves work units to requesting clients.
   - Handles data staging coordination with Shock.

2. **AWE Client (Worker)**:
   - Registers with the server.
   - Pulls (checks out) work units from the server queue.
   - Downloads input data from Shock.
   - Executes commands (typically shell scripts/programs).
   - Uploads output data to Shock.
   - Reports completion/failure back to the server.

3. **Task Partitioning**:
   - Tasks can be split into work units based on data partitions.
   - Enables scatter-gather parallelism at the data level.
   - Work units are independently dispatched to available clients.

#### Scheduling Approach

- **Pull-based work queue**: Clients request work rather than the server pushing tasks, providing natural load balancing.
- **Client groups**: Tasks can be restricted to specific client groups (e.g., high-memory nodes).
- **Priority**: Job-level priority affects queue ordering.
- **FIFO within priority**: First-come, first-served within the same priority level.
- **Data locality**: Can be configured to prefer clients close to the data (Shock nodes).

#### Data/File Management

- **Shock integration**: Deep integration with Shock, a scientific data management system.
  - Data is stored in Shock nodes with metadata and access control.
  - Inputs are downloaded from Shock before task execution.
  - Outputs are uploaded to Shock after task completion.
  - Supports data partitioning (splitting large files for parallel processing).
- **File references**: Inputs/outputs reference Shock URLs and node IDs.
- **Data provenance**: Shock nodes maintain provenance information linking outputs to the jobs/tasks that produced them.

#### Monitoring Capabilities

- **REST API queries**: Job and task status available via API.
- **Job states**: Detailed state tracking (submitted, queued, in-progress, completed, suspended, deleted).
- **Task states**: Per-task state tracking within jobs.
- **Client status**: Monitor registered clients, their status, and current work.
- **Work unit tracking**: Fine-grained tracking of individual work units within tasks.
- **Basic web UI**: Simple web interface for job monitoring (not as sophisticated as Airflow's).
- **Logging**: Server and client logging for debugging.

#### API/Programmatic Access

AWE's **REST API** is the primary interface:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/job` | POST | Submit a new job |
| `/job/{id}` | GET | Get job status/details |
| `/job/{id}` | PUT | Update job (suspend, resume, resubmit) |
| `/job/{id}` | DELETE | Delete a job |
| `/job?query_params` | GET | List/search jobs |
| `/work` | GET | Checkout a work unit (for clients) |
| `/work/{id}` | PUT | Report work unit completion |
| `/client` | GET | List registered clients |
| `/client/{id}` | GET | Get client details |
| `/queue` | GET | Queue status and statistics |
| `/logger` | GET/PUT | View/modify logging settings |

#### Container Support

- **Full Docker container support**: The AWE worker natively supports per-task Docker container execution.
- Jobs specify containers via two fields in the `Command` struct:
  - `Dockerimage`: Docker image hosted in Shock (downloaded, loaded via `docker load`).
  - `DockerPull`: Docker image pulled from a registry (e.g., Docker Hub), aligning with CWL's `dockerPull` convention.
- The worker handles the full container lifecycle: image retrieval, container creation, start, wait, and cleanup.
- **Data staging**: The worker downloads input data to the host, then **bind-mounts** the work directory into the container at `/workdir/`. Pre-data (reference databases) is bind-mounted read-only at `/db/`.
- **Two execution modes**: Docker API (via `go-dockerclient` library) or Docker CLI binary, configurable via `docker_binary` setting.
- **Memory monitoring**: Optional cgroup-based memory profiling of running containers (`total_rss`, `total_swap`).
- **CWL integration**: For CWL workunits, the worker delegates to `cwl-runner` (cwltool), which manages its own Docker containers via `DockerRequirement`.
- Workers can also be deployed inside Docker containers themselves (Docker-in-Docker or socket mounting).
- Configuration options: `use_docker` (`yes`/`no`/`only`), `docker_socket`, `docker_workpath`, `docker_data`, `image_url` (Shock image server).

#### Extensibility/Plugin Model

- **Command-based**: Any executable can be a task command; extensibility is through external tools.
- **Client groups**: Different client configurations for different workload types.
- **No formal plugin system**: Extension is primarily through the REST API and external integration.
- **Shock plugins**: Data management extensions via Shock.

#### Architecture Diagram (Conceptual)

```
                    REST API
                       |
                +-----------+
                | AWE Server|
                |-----------|
                | Job Queue |
                | Task DAG  |        +----------+
                | Work Queue|<------>|  MongoDB  |
                | Scheduler |        +----------+
                +-----------+
                 /    |    \
                /     |     \
         +--------+ +--------+ +--------+
         |Client 1| |Client 2| |Client 3|
         +--------+ +--------+ +--------+
              |          |          |
         +--------+ +--------+ +--------+
         | Shock  | | Shock  | | Shock  |
         | (data) | | (data) | | (data) |
         +--------+ +--------+ +--------+
```

---

## 2. Comparative Analysis Table

| Feature | Nextflow | Snakemake | Apache Airflow | Parsl | AWE |
|---------|----------|-----------|----------------|-------|-----|
| **Workflow Definition** | Groovy DSL (DSL2) | Python-based Snakefile (make-like rules) | Python code (DAG objects, TaskFlow decorators) | Python decorators (@python_app, @bash_app) | JSON job documents |
| **Paradigm** | Dataflow (channels) | Rule-based (file targets) | DAG-based (task dependencies) | Futures-based (implicit dataflow) | Task-queue (explicit dependencies) |
| **Implementation Language** | Groovy/Java (JVM) | Python | Python | Python | **Go** |
| **Scheduler Type** | Central (per-run) | Central (per-run) | Central (persistent service) | Central (DFK, per-script) | Central (persistent service) |
| **State Management** | File-based cache (.nextflow/) | File timestamps + metadata | Relational database (PostgreSQL/MySQL) | In-memory + monitoring DB (SQLite) | MongoDB |
| **REST API** | Via Seqera Platform only | No (CLI + Python API) | **Yes** (comprehensive, stable) | No (library API) | **Yes** (comprehensive) |
| **RPC API** | No | No | No (REST only) | No | No (REST only) |
| **Container Support** | **Excellent** (Docker, Singularity, Conda, Wave) | **Good** (Singularity, Docker, Conda) | **Good** (Docker, Kubernetes pods) | Moderate (Singularity, Docker) | **Good** (native per-task Docker via worker; CWL DockerRequirement) |
| **Cloud Support** | AWS, GCP, Azure (native) | AWS, GCP, Azure (via plugins) | AWS, GCP, Azure (via providers) | AWS, GCP (via providers) | Manual deployment |
| **HPC Support** | SLURM, PBS, SGE, LSF | SLURM, PBS, SGE, LSF | Limited (via SSH/custom operators) | **Excellent** (SLURM, PBS, Cobalt, Flux) | Client deployment on HPC |
| **Monitoring/UI** | Seqera Platform (external), HTML reports | Panoptes (external), HTML reports | **Built-in web UI** (rich) | Built-in dashboard (basic) | Basic web UI, REST API |
| **Fault Tolerance** | Retry, error strategy, resume | Retry, rerun-incomplete | Retry, SLA, alerting, callbacks | Retry, checkpoint, fault handlers | Retry, resubmit, client failover |
| **Scalability** | Thousands of tasks | Thousands of tasks | Millions of task instances | Thousands of concurrent tasks | Thousands of work units |
| **Learning Curve** | Moderate (Groovy DSL) | Low-Moderate (Python + make concepts) | Moderate-High (many concepts, operators) | Low (Python decorators) | Low (JSON + REST) |
| **Community Size** | Very large (bioinformatics) | Very large (bioinformatics) | **Very large** (general data engineering) | Medium (scientific computing) | Small (MG-RAST ecosystem) |
| **Community Activity** | Very active (nf-core) | Very active | **Very active** (Apache project) | Active | Low (maintenance mode) |
| **Primary Domain** | Bioinformatics, genomics | Bioinformatics, data analysis | Data engineering, ETL, ML ops | Scientific computing, HPC | Bioinformatics (MG-RAST) |
| **CWL Compatibility** | Via conversion tools | Via conversion tools | No | No | No |
| **Data Provenance** | Execution trace, reports | Built-in provenance tracking | Audit logs, XCom history | Monitoring database | Via Shock metadata |
| **Dynamic Workflows** | DSL2 subworkflows, conditional | Checkpoints, dynamic rules | Dynamic task generation in Python | join_apps for dynamic graphs | Static job documents |
| **Multi-tenancy** | Via Seqera Platform | No | **Yes** (RBAC, DAG-level permissions) | No | Basic (user-based) |

---

## 3. Common Concepts

The following concepts appear across all or most of the five workflow engines. These represent fundamental patterns that any workflow engine -- including GoWe -- should address.

### 3.1 DAG Representation

All five engines represent workflows as **Directed Acyclic Graphs (DAGs)**, though they arrive at the DAG differently:

- **Explicit DAG**: Airflow and AWE require users to explicitly define task dependencies.
- **Implicit DAG (dataflow)**: Nextflow infers the DAG from channel connections between processes.
- **Implicit DAG (file targets)**: Snakemake infers the DAG by resolving input/output file relationships backward from targets.
- **Implicit DAG (futures)**: Parsl builds the DAG at runtime when futures are passed between app invocations.

**GoWe implication**: Support both explicit dependency declaration (like AWE's `dependsOn`) and the ability to infer dependencies from input/output declarations.

### 3.2 Task/Process Abstraction

Every engine has a concept of an atomic unit of work:

| Engine | Term | Definition |
|--------|------|------------|
| Nextflow | Process | A unit of computation with declared inputs, outputs, directives, and a script |
| Snakemake | Rule | A transformation from input files to output files |
| Airflow | Task/Operator | An instance of an operator class configured for a specific operation |
| Parsl | App | A decorated Python or Bash function |
| AWE | Task | A command with inputs, outputs, and dependencies |

Common properties across all:
- **Isolation**: Tasks run independently with defined boundaries.
- **Declarative I/O**: Inputs and outputs are explicitly declared.
- **Resource requirements**: CPU, memory, time, and custom resources.
- **Retry semantics**: All support some form of retry on failure.

**GoWe implication**: Define a `Task` type with inputs, outputs, command specification, resource requirements, and retry policy.

### 3.3 Input/Output Binding

All engines manage how data flows between tasks:

- **File-based**: Snakemake and AWE primarily use files as the data interface between tasks.
- **Channel-based**: Nextflow uses channels (FIFO queues) that carry file references, values, or tuples.
- **Future-based**: Parsl passes data via Python futures (in-memory for Python apps, files for Bash apps).
- **XCom/External**: Airflow passes small metadata via XComs; large data via external systems.

**GoWe implication**: Support file-based I/O binding with optional metadata passing. Files are the natural interface for bioinformatics tools.

### 3.4 Dependency Resolution

All engines resolve task execution order:

- **Forward resolution**: Nextflow and Parsl propagate data forward -- a task runs when its inputs are available.
- **Backward resolution**: Snakemake resolves backward from the requested target to determine what needs to run.
- **Explicit ordering**: Airflow and AWE use explicitly declared dependencies.
- **Hybrid**: Most engines combine approaches (e.g., AWE has explicit dependencies, but within a task, data availability drives execution).

**GoWe implication**: Implement a topological sort of the task DAG with dependency checking before each task dispatch.

### 3.5 Resource Requirements

All engines support declaring resource needs:

- CPUs/threads
- Memory
- Wall time / execution time limits
- GPU requirements (Nextflow, Snakemake, Parsl)
- Custom resources (queue names, licenses, etc.)

**GoWe implication**: Include a `Resources` struct in task definitions that maps to BV-BRC job parameters.

### 3.6 Containerization

All modern engines support containers to varying degrees:

- **Per-task containers**: Nextflow, Snakemake, and AWE allow different containers per process/rule/task.
- **Per-executor containers**: Airflow (KubernetesExecutor) and Parsl use containers at the executor level.
- **AWE per-task containers**: Each task's `Command` specifies a `Dockerimage` (Shock-hosted) or `DockerPull` (registry) field. The worker stages data via bind-mounts and runs the task inside the specified container.

**GoWe implication**: Support per-task container specification in workflow definitions, even if initial execution delegates to BV-BRC's execution environment.

### 3.7 Logging and Provenance

All engines track execution history:

- **Execution logs**: Per-task stdout/stderr capture.
- **Metadata**: Start time, end time, exit code, resource usage.
- **Provenance**: Which inputs produced which outputs via which commands.
- **Persistence**: Stored in files (Nextflow, Snakemake), databases (Airflow, Parsl), or external systems (AWE/Shock).

**GoWe implication**: Log task execution metadata to a database and capture stdout/stderr for each task.

### 3.8 Retry and Error Handling

All engines provide failure recovery:

- **Automatic retry**: Configurable retry count with optional backoff.
- **Error strategies**: Nextflow offers `terminate`, `finish`, `ignore`, and `retry` strategies.
- **Resume**: Nextflow and Snakemake can resume from the last successful task.
- **Manual resubmission**: AWE and Airflow allow manual retry of failed tasks.
- **Callbacks/handlers**: Airflow and Parsl support custom error handling logic.

**GoWe implication**: Implement configurable retry with exponential backoff, and support workflow resume from last successful state.

---

## 4. Differentiating Concepts

### 4.1 Nextflow's Dataflow Channels

Nextflow's most distinctive feature is its **channel-based dataflow model**:

- **Channels as first-class citizens**: Data flows through typed channels that connect processes.
- **Channel operators**: A rich library of operators transforms data streams (map, filter, collect, join, combine, cross, merge, branch, multiMap).
- **Reactive execution**: Processes fire automatically when channel data arrives -- no polling or explicit scheduling needed.
- **Fan-out/fan-in**: Natural support for scatter-gather patterns via channel operators.
- **Value channels vs. queue channels**: Value channels can be read multiple times; queue channels are consumed.

**Why it matters for GoWe**: The channel model elegantly handles bioinformatics patterns where a single sample flows through multiple analysis steps, and results from many samples are aggregated. Consider implementing a channel-like abstraction for data flow between BV-BRC jobs.

### 4.2 Snakemake's Rule-Based Make Approach

Snakemake's differentiation lies in its **declarative, target-driven execution**:

- **Output-first thinking**: Users declare what they want to produce, and Snakemake figures out how.
- **Wildcard pattern matching**: `{sample}` wildcards generalize rules across datasets automatically.
- **Incremental computation**: Only re-runs tasks whose inputs have changed (timestamp-based, like Make).
- **Checkpoints**: Rules that dynamically alter the DAG based on their output (e.g., discovering how many chunks to process).
- **Benchmark tracking**: Built-in resource usage benchmarking per rule.

**Why it matters for GoWe**: The target-driven approach is powerful for bioinformatics where users think in terms of "I want assembly results for these 50 samples." Wildcard-based task generation could simplify batch BV-BRC job submission.

### 4.3 Airflow's Operator/Plugin Ecosystem

Airflow's key differentiator is its **enterprise-grade orchestration platform** with:

- **Operator library**: 70+ provider packages with hundreds of operators for external systems.
- **Sensor pattern**: Built-in pattern for waiting on external conditions (file arrival, API response, database query).
- **Scheduling engine**: Production-grade scheduler with time-based, dataset-triggered, and manual execution.
- **Multi-tenancy**: RBAC, audit logs, and DAG-level access control.
- **Connection management**: Centralized, encrypted credential storage.
- **Templating**: Jinja2 templates in operator parameters for dynamic configuration.

**Why it matters for GoWe**: Airflow's operator pattern maps naturally to BV-BRC job types. Each BV-BRC service (Assembly, Annotation, etc.) could be a GoWe "operator." Airflow's REST API design is an excellent reference for GoWe's API.

### 4.4 Parsl's Python-Native Futures

Parsl's distinction is **pure Python parallelism without a DSL**:

- **Decorator-based**: `@python_app` and `@bash_app` decorators transparently parallelize functions.
- **Futures composition**: Dependencies emerge naturally from passing futures as arguments.
- **Join apps**: `@join_app` enables dynamic workflow patterns where an app returns futures to other apps.
- **Multi-site execution**: A single script can dispatch tasks to multiple heterogeneous resources simultaneously.
- **Elastic resource management**: Providers automatically scale compute blocks based on queue depth.

**Why it matters for GoWe**: Parsl's separation of application logic from execution configuration (via Config objects) is an excellent pattern. The elastic scaling model could inform how GoWe manages worker pools for BV-BRC job execution.

### 4.5 AWE's Go-Native Client-Worker Model

AWE's differentiation comes from being **purpose-built in Go for bioinformatics**:

- **Pull-based work queue**: Workers pull tasks rather than having tasks pushed to them -- providing natural load balancing and fault tolerance.
- **Go concurrency model**: Leverages goroutines and Go channels for efficient server-side task management.
- **Native Docker container execution**: Workers manage the full container lifecycle -- image retrieval (from Shock or registry), data staging via bind-mounts (`/workdir/`, `/db/`), container creation/start/wait, and output collection. Supports both Docker API (`go-dockerclient`) and CLI binary modes.
- **Shock data integration**: Deep coupling with a scientific data management system for provenance-aware data handling.
- **JSON job documents**: Simple, portable workflow definitions that can be generated programmatically.
- **Work unit partitioning**: Tasks can be split into multiple work units for data-parallel execution.
- **Lightweight clients**: Workers are simple Go binaries that can be deployed anywhere.
- **CWL support**: For CWL workunits, workers delegate to `cwl-runner` (cwltool), which manages its own Docker containers via `DockerRequirement`.

**Why it matters for GoWe**: AWE is the most directly relevant architecture. Its client-server model, REST API design, JSON job format, Go implementation, and native Docker container execution (with worker-managed data staging and bind-mounts) provide a concrete reference. AWE's limitations (Docker-only runtime, limited dynamic workflows, small community) indicate where GoWe should improve (e.g., adding Singularity/Podman support, dynamic DAGs).

---

## 5. Ranking for BV-BRC Integration

### Scoring Criteria

Each engine is scored 1-10 on seven criteria, with weights reflecting importance for the GoWe use case.

#### Criteria Definitions

1. **Adaptability (Weight: 25%)** - How easily can the engine's design patterns be adapted for a custom Go implementation? Considers: language compatibility, architectural simplicity, modularity, and presence of clean abstractions that translate well to Go.

2. **Community & Support (Weight: 10%)** - Size of community, activity level, documentation quality, and availability of learning resources. Higher scores for larger, more active communities with better documentation.

3. **BV-BRC Integration Potential (Weight: 25%)** - How well does the engine's architecture align with BV-BRC's JSON-RPC job submission API? Considers: API-first design, JSON-based workflow definitions, support for external job submission, and bioinformatics domain fit.

4. **Workflow Definition Portability (Weight: 5%)** - Can workflow definitions be used across systems? CWL/WDL compatibility, standardization, and ease of translation between formats.

5. **Monitoring & Management (Weight: 15%)** - Built-in capabilities for job tracking, logging, status reporting, and operational management. Higher scores for comprehensive, API-accessible monitoring.

6. **Scalability (Weight: 10%)** - Ability to handle many concurrent jobs, scale workers, and manage large workflow graphs. Considers both horizontal and vertical scaling patterns.

7. **Implementation Complexity (Weight: 10%)** - How complex would it be to implement similar concepts in Go? Lower complexity scores higher. Considers Go idiom compatibility, required infrastructure, and third-party dependencies.

### Detailed Scoring

#### 1. AWE

| Criterion | Score | Justification |
|-----------|-------|---------------|
| Adaptability | **9** | Already written in Go. Architecture can be directly studied and adapted. Client-server model with goroutines is idiomatic Go. JSON job format is simple and extensible. |
| Community & Support | **3** | Small community, limited documentation, low activity. Primarily used within MG-RAST ecosystem. |
| BV-BRC Integration | **9** | Designed for bioinformatics job submission. JSON-based workflows. REST API-first design. Client model maps well to BV-BRC's job execution pattern. Shock integration analogous to BV-BRC workspace. |
| Definition Portability | **3** | Proprietary JSON format. No CWL/WDL compatibility. Limited adoption outside MG-RAST. |
| Monitoring & Management | **5** | REST API for status queries. Basic web UI. Limited compared to Airflow. Adequate for programmatic monitoring. |
| Scalability | **7** | Pull-based queue scales well with workers. Go's efficiency handles high throughput. Limited by single-server architecture. |
| Implementation Complexity | **9** | Already in Go -- lowest barrier to adaptation. Minimal external dependencies (MongoDB). Clean, understandable codebase. |

**Weighted Score**: 9(0.25) + 3(0.10) + 9(0.25) + 3(0.05) + 5(0.15) + 7(0.10) + 9(0.10) = 2.25 + 0.30 + 2.25 + 0.15 + 0.75 + 0.70 + 0.90 = **7.30**

#### 2. Apache Airflow

| Criterion | Score | Justification |
|-----------|-------|---------------|
| Adaptability | **7** | Excellent architecture that translates well to Go. Clean separation of scheduler, executor, web server, and database. Operator pattern is language-agnostic. REST API design is exemplary. |
| Community & Support | **10** | Largest community. Apache project with corporate backing. Extensive documentation. Thousands of contributors. |
| BV-BRC Integration | **7** | HttpOperator can call JSON-RPC APIs. Strong orchestration model. But designed for ETL/data engineering, not scientific computing. Would need custom operators for BV-BRC. |
| Definition Portability | **4** | Python-only DAG definitions. Not portable to other engines. No CWL compatibility. |
| Monitoring & Management | **10** | Best-in-class web UI. Comprehensive REST API. Metrics, alerting, audit logs, SLA tracking. |
| Scalability | **9** | Production-proven at massive scale. Multiple executor options. Celery/Kubernetes for horizontal scaling. |
| Implementation Complexity | **5** | Complex architecture (scheduler, web server, metadata DB, message broker). Many moving parts. Significant effort to replicate core features in Go. |

**Weighted Score**: 7(0.25) + 10(0.10) + 7(0.25) + 4(0.05) + 10(0.15) + 9(0.10) + 5(0.10) = 1.75 + 1.00 + 1.75 + 0.20 + 1.50 + 0.90 + 0.50 = **7.60**

#### 3. Nextflow

| Criterion | Score | Justification |
|-----------|-------|---------------|
| Adaptability | **6** | Dataflow model is elegant but complex to implement. Channel operators require significant infrastructure. JVM-specific patterns don't translate directly to Go. Process abstraction is clean. |
| Community & Support | **9** | Very large bioinformatics community. nf-core ecosystem. Excellent documentation. Active development. |
| BV-BRC Integration | **6** | Bioinformatics-native but designed for direct execution rather than job submission to external APIs. Would need custom executors for BV-BRC integration. |
| Definition Portability | **5** | Groovy DSL is proprietary. nf-core standardization helps. Some CWL interop tools exist. |
| Monitoring & Management | **7** | Seqera Platform is excellent but external/commercial. Built-in HTML reports and trace files. No built-in REST API. |
| Scalability | **8** | Handles large pipelines well. Multiple executor backends. Seqera Platform adds enterprise scaling. |
| Implementation Complexity | **4** | Channel/dataflow model is complex to implement correctly. Operator library is extensive. Groovy DSL parsing is JVM-specific. |

**Weighted Score**: 6(0.25) + 9(0.10) + 6(0.25) + 5(0.05) + 7(0.15) + 8(0.10) + 4(0.10) = 1.50 + 0.90 + 1.50 + 0.25 + 1.05 + 0.80 + 0.40 = **6.40**

#### 4. Parsl

| Criterion | Score | Justification |
|-----------|-------|---------------|
| Adaptability | **5** | Python-specific patterns (decorators, futures) don't directly translate to Go. However, the DFK concept, provider/executor separation, and elastic scaling are applicable. |
| Community & Support | **6** | Medium community. Good documentation. Active development. Strong in HPC/scientific computing. |
| BV-BRC Integration | **5** | Designed for direct computation execution, not orchestrating external job APIs. Provider model could be extended, but core is local/HPC execution. |
| Definition Portability | **3** | Pure Python -- completely tied to the Python ecosystem. No portability to other engines. |
| Monitoring & Management | **5** | Built-in monitoring hub with SQLite backend. Basic visualization. No REST API for monitoring. |
| Scalability | **8** | Excellent HPC scaling via HighThroughputExecutor. Elastic resource management. Multi-site support. |
| Implementation Complexity | **6** | DFK and executor concepts are moderate complexity. Provider/launcher abstraction is clean. Futures model needs adaptation for Go (channels/goroutines). |

**Weighted Score**: 5(0.25) + 6(0.10) + 5(0.25) + 3(0.05) + 5(0.15) + 8(0.10) + 6(0.10) = 1.25 + 0.60 + 1.25 + 0.15 + 0.75 + 0.80 + 0.60 = **5.40**

#### 5. Snakemake

| Criterion | Score | Justification |
|-----------|-------|---------------|
| Adaptability | **5** | Rule-based, file-centric model is conceptually simple but deeply tied to Python. Wildcard resolution and DAG inference are complex to reimplement. Plugin architecture (v8) is more modular. |
| Community & Support | **9** | Very large bioinformatics community. Excellent documentation. Active development. Workflow catalog. |
| BV-BRC Integration | **5** | File-centric model works for bioinformatics but assumes direct execution. No built-in support for external job APIs. Would need significant adaptation for BV-BRC JSON-RPC. |
| Definition Portability | **5** | Snakefile format is proprietary but well-established. Some CWL conversion tools. Workflow wrappers aid reuse. |
| Monitoring & Management | **5** | Panoptes for monitoring. HTML reports. CLI-driven status. No built-in REST API. |
| Scalability | **7** | Good scaling via cluster execution. Executor plugins for various backends. Single-coordinator bottleneck. |
| Implementation Complexity | **5** | Wildcard resolution and backward DAG inference are intellectually complex. File-timestamp logic adds edge cases. Plugin system (v8) is more modular. |

**Weighted Score**: 5(0.25) + 9(0.10) + 5(0.25) + 5(0.05) + 5(0.15) + 7(0.10) + 5(0.10) = 1.25 + 0.90 + 1.25 + 0.25 + 0.75 + 0.70 + 0.50 = **5.60**

### Final Ranking

| Rank | Engine | Weighted Score | Summary Justification |
|------|--------|---------------|----------------------|
| **1** | **Apache Airflow** | **7.60** | Best monitoring/management, excellent API design, strong architecture that translates to Go. Operator pattern maps naturally to BV-BRC services. Largest general community. |
| **2** | **AWE** | **7.30** | Already in Go, designed for bioinformatics, REST API-first, JSON job format. Closest to GoWe's target architecture. Limited by small community and aging codebase. |
| **3** | **Nextflow** | **6.40** | Dominant in bioinformatics, excellent dataflow model, strong container support. But JVM-centric and complex to reimplement. |
| **4** | **Snakemake** | **5.60** | Strong bioinformatics community, elegant rule-based model. But deeply Python-specific and CLI-focused without a server API. |
| **5** | **Parsl** | **5.40** | Clean execution abstractions and excellent HPC support. But tightly coupled to Python and not designed for API-based orchestration. |

---

## 6. Recommendations for GoWe

### 6.1 Design Patterns to Adopt from Each Engine

#### From AWE (Primary Reference)
- **Client-server architecture**: GoWe should use AWE's proven pattern of a central server with pull-based worker clients.
- **JSON job documents**: Adopt and extend AWE's JSON workflow definition format.
- **REST API-first design**: All operations via REST API, making GoWe programmable from any language.
- **Go concurrency patterns**: Use goroutines for the scheduler, channels for internal communication, and Go's HTTP server for the API.
- **Work unit model**: Support task partitioning into work units for data-parallel execution.

#### From Apache Airflow (Monitoring & Management)
- **Operator pattern**: Define a `BVBRCOperator` concept where each BV-BRC service (Assembly, Annotation, GenomeComparison, etc.) is a typed operator with validated parameters.
- **Task state machine**: Adopt Airflow's well-defined task lifecycle: `none -> scheduled -> queued -> running -> success/failed/up_for_retry/skipped`.
- **REST API design**: Model GoWe's API on Airflow's comprehensive REST API (CRUD for workflows, runs, tasks, plus monitoring endpoints).
- **Connection management**: Centralized BV-BRC credential and endpoint management.
- **Monitoring patterns**: Task instance logging, execution metrics, and status aggregation.
- **Pool/queue management**: Resource pools to limit concurrent BV-BRC job submissions.

#### From Nextflow (Workflow Execution)
- **Process isolation**: Each task runs in an isolated work directory with staged inputs.
- **Resume/caching**: Hash-based caching of task results for workflow resume on failure.
- **Container specification**: Per-task container declarations in workflow definitions.
- **Error strategies**: Configurable per-task error handling (retry, ignore, terminate workflow).
- **Execution reports**: Automated HTML reports with timeline and resource usage.

#### From Snakemake (Workflow Definition)
- **Target-driven execution**: Support "give me output X" and let the engine determine what needs to run.
- **Wildcard patterns**: Template-based task generation for batch processing (e.g., run Assembly for all samples matching `{sample_id}`).
- **Incremental computation**: Only re-run tasks whose inputs have changed.
- **Config files**: YAML/JSON configuration files that parameterize workflows without modifying definitions.

#### From Parsl (Execution Management)
- **Provider/executor separation**: Clean separation between "how to run tasks" (executor) and "where to get compute" (provider).
- **Elastic scaling**: Automatically scale worker pools based on queue depth.
- **Resource specification**: Per-task resource requirements that influence scheduling decisions.
- **Multi-site execution**: Support dispatching different tasks to different execution backends.

### 6.2 Proposed Architecture for GoWe

```
+------------------------------------------------------------------+
|                         GoWe Architecture                         |
+------------------------------------------------------------------+

                        +------------------+
                        |    GoWe CLI      |
                        | (cmd/cli)        |
                        | - Submit workflows|
                        | - Query status   |
                        | - Manage configs |
                        +--------+---------+
                                 |
                            REST API / gRPC
                                 |
                        +--------v---------+
                        |   GoWe Server    |
                        |   (cmd/server)   |
                        +------------------+
                        |                  |
            +-----------+   +-----------+  +-----------+
            | API Layer |   | Workflow   |  | Monitor   |
            | (REST +   |   | Engine    |  | Service   |
            |  JSON-RPC)|   |           |  |           |
            +-----------+   +-----------+  +-----------+
                        |                  |
            +-----------+   +-----------+  +-----------+
            | Workflow   |   | Scheduler |  | State     |
            | Validator  |   | (cmd/     |  | Store     |
            |            |   | scheduler)|  | (DB)      |
            +-----------+   +-----------+  +-----------+
                                 |
                    +------------+------------+
                    |            |            |
              +-----v----+ +----v-----+ +----v-----+
              | Local     | | BV-BRC   | | HPC      |
              | Executor  | | Executor | | Executor |
              +-----------+ +----------+ +----------+
                            |
                      +-----v------+
                      | BV-BRC     |
                      | JSON-RPC   |
                      | API Client |
                      +-----+------+
                            |
                      +-----v------+
                      | BV-BRC     |
                      | Services   |
                      +------------+
```

#### Core Components

1. **GoWe CLI** (`cmd/cli`): Command-line interface for workflow submission, status queries, and configuration management. Communicates with the server via REST API.

2. **GoWe Server** (`cmd/server`): Persistent service hosting:
   - **API Layer**: REST API (primary) + JSON-RPC bridge for BV-BRC compatibility.
   - **Workflow Engine**: Parses workflow definitions, validates them, constructs DAGs, and manages lifecycle.
   - **Workflow Validator**: Schema validation, dependency cycle detection, BV-BRC parameter validation.
   - **Monitor Service**: Aggregates task status, provides real-time updates, generates reports.

3. **GoWe Scheduler** (`cmd/scheduler`): Can run embedded in the server or as a separate process:
   - Evaluates task readiness (all dependencies satisfied).
   - Dispatches tasks to appropriate executors.
   - Manages resource pools and concurrency limits.
   - Handles retry logic and error strategies.

4. **Executors** (pluggable):
   - **LocalExecutor**: Run tasks as local processes (for development/testing).
   - **BVBRCExecutor**: Submit tasks as BV-BRC jobs via JSON-RPC API, poll for completion.
   - **HPCExecutor**: Submit tasks to SLURM/PBS (future).
   - **ContainerExecutor**: Run tasks in Docker/Singularity containers (future).

5. **State Store**: Persistent storage for workflow state, task history, and monitoring data:
   - **SQLite** for single-server deployment.
   - **PostgreSQL** for production multi-server deployment.
   - Stores: workflow definitions, run instances, task states, execution logs, metrics.

#### Key Go Packages

```
pkg/
  workflow/         # Workflow definition types and parser
    definition.go   # Workflow, Task, Input, Output types
    parser.go       # JSON/YAML workflow parser
    validator.go    # Validation logic
    dag.go          # DAG construction and topological sort

  engine/           # Workflow execution engine
    engine.go       # Core engine (DAG walking, state management)
    state.go        # State machine for tasks and workflows

  scheduler/        # Task scheduling
    scheduler.go    # Scheduler interface and implementation
    queue.go        # Priority work queue
    pool.go         # Resource pool management

  executor/         # Execution backends
    executor.go     # Executor interface
    local.go        # Local process execution
    bvbrc.go        # BV-BRC JSON-RPC executor

  api/              # REST API handlers
    server.go       # HTTP server setup
    workflows.go    # Workflow CRUD endpoints
    runs.go         # Run management endpoints
    tasks.go        # Task status endpoints
    monitor.go      # Monitoring endpoints

  store/            # Persistence layer
    store.go        # Store interface
    sqlite.go       # SQLite implementation
    postgres.go     # PostgreSQL implementation

  monitor/          # Monitoring and logging
    monitor.go      # Monitor service
    metrics.go      # Metrics collection
    reporter.go     # Report generation

  bvbrc/            # BV-BRC integration
    client.go       # JSON-RPC client
    types.go        # BV-BRC job types
    auth.go         # Authentication
```

### 6.3 Workflow Definition Format Recommendation

**Recommendation**: Use **JSON as the primary format** with **YAML as an alternative**, drawing from AWE's job document structure but enriched with concepts from Airflow and Nextflow.

Rationale:
- JSON is native to Go's standard library and BV-BRC's JSON-RPC API.
- JSON Schema provides built-in validation.
- YAML is more human-friendly for manual editing and is a superset of JSON.
- Avoid creating a custom DSL -- it increases learning curve and implementation complexity.

#### Proposed Workflow Definition Schema

```json
{
    "$schema": "https://gowe.example.com/schema/workflow/v1",
    "version": "1.0",
    "workflow": {
        "id": "genome-assembly-annotation",
        "name": "Genome Assembly and Annotation Pipeline",
        "description": "Assemble raw reads and annotate the resulting contigs",
        "author": "researcher@university.edu",
        "tags": ["genomics", "assembly", "annotation"],

        "params": {
            "min_contig_length": {
                "type": "integer",
                "default": 500,
                "description": "Minimum contig length to retain"
            },
            "recipe": {
                "type": "string",
                "enum": ["auto", "unicycler", "spades", "megahit"],
                "default": "auto"
            }
        },

        "inputs": {
            "reads_1": {
                "type": "file",
                "required": true,
                "description": "Forward reads (FASTQ)"
            },
            "reads_2": {
                "type": "file",
                "required": true,
                "description": "Reverse reads (FASTQ)"
            }
        },

        "tasks": [
            {
                "id": "assembly",
                "name": "Genome Assembly",
                "type": "bvbrc:GenomeAssembly2",
                "depends_on": [],
                "inputs": {
                    "paired_end_libs": [
                        {
                            "read1": "{{workflow.inputs.reads_1}}",
                            "read2": "{{workflow.inputs.reads_2}}"
                        }
                    ]
                },
                "params": {
                    "recipe": "{{workflow.params.recipe}}",
                    "min_contig_length": "{{workflow.params.min_contig_length}}",
                    "output_path": "/researcher@university.edu/assemblies",
                    "output_file": "assembly_results"
                },
                "outputs": {
                    "contigs": {
                        "path": "{{task.params.output_path}}/{{task.params.output_file}}/contigs.fasta"
                    }
                },
                "resources": {
                    "queue": "default",
                    "max_runtime": "4h"
                },
                "retry": {
                    "max_attempts": 2,
                    "backoff": "exponential",
                    "delay": "5m"
                }
            },
            {
                "id": "annotation",
                "name": "Genome Annotation",
                "type": "bvbrc:GenomeAnnotation",
                "depends_on": ["assembly"],
                "inputs": {
                    "contigs": "{{tasks.assembly.outputs.contigs}}"
                },
                "params": {
                    "scientific_name": "Escherichia coli",
                    "taxonomy_id": 562,
                    "genetic_code": 11,
                    "domain": "Bacteria",
                    "output_path": "/researcher@university.edu/annotations",
                    "output_file": "annotation_results"
                },
                "outputs": {
                    "genome": {
                        "path": "{{task.params.output_path}}/{{task.params.output_file}}"
                    }
                },
                "resources": {
                    "queue": "default",
                    "max_runtime": "2h"
                },
                "retry": {
                    "max_attempts": 2,
                    "backoff": "exponential",
                    "delay": "5m"
                }
            }
        ],

        "outputs": {
            "contigs": "{{tasks.assembly.outputs.contigs}}",
            "annotated_genome": "{{tasks.annotation.outputs.genome}}"
        },

        "on_success": {
            "notify": ["email:researcher@university.edu"]
        },
        "on_failure": {
            "notify": ["email:researcher@university.edu"],
            "strategy": "finish_running"
        }
    }
}
```

Key design decisions:
- **Template expressions** (`{{...}}`): For referencing workflow inputs, parameters, and task outputs -- inspired by Airflow's Jinja templating.
- **Task types** (`bvbrc:GenomeAssembly2`): Namespaced types that map to BV-BRC services, similar to Airflow operators.
- **Explicit dependencies** (`depends_on`): Simple and unambiguous, like AWE.
- **Parameterization**: Workflow-level params that flow into tasks, like Snakemake's config.
- **Resource specification**: Per-task resource requirements.
- **Error strategies**: Per-task and workflow-level error handling, inspired by Nextflow.

### 6.4 Scheduler Design Recommendation

**Recommendation**: Implement a **central scheduler with a priority work queue**, combining AWE's pull-based model with Airflow's state machine.

#### Scheduler Architecture

```
Scheduler Loop (runs continuously as a goroutine):

1. SCAN: Query state store for workflows in "running" state
2. EVALUATE: For each running workflow:
   a. Get all tasks in "pending" state
   b. For each pending task, check if all depends_on tasks are "success"
   c. If ready, transition to "scheduled" and add to work queue
3. DISPATCH: For each item in work queue (respecting pool limits):
   a. Select appropriate executor based on task type
   b. Submit task to executor
   c. Transition task to "running"
4. POLL: For each task in "running" state:
   a. Query executor for current status
   b. If completed: transition to "success", check workflow completion
   c. If failed: apply retry policy or transition to "failed"
   d. If failed + no retries: evaluate workflow error strategy
5. FINALIZE: For workflows where all tasks are terminal:
   a. If all success: transition workflow to "success"
   b. If any failed: transition workflow to "failed"
   c. Execute on_success / on_failure handlers
6. SLEEP: Wait for configurable interval (default 5 seconds)
7. GOTO 1
```

#### Task State Machine

```
         +--------+
         | NONE   |  (task defined but workflow not started)
         +---+----+
             |
             v
         +--------+
    +--->| PENDING|  (waiting for dependencies)
    |    +---+----+
    |        |
    |        v  (all dependencies satisfied)
    |    +----------+
    |    |SCHEDULED |  (in work queue, awaiting dispatch)
    |    +---+------+
    |        |
    |        v  (dispatched to executor)
    |    +--------+
    |    |RUNNING |  (executing on BV-BRC or local)
    |    +---+----+
    |        |
    |   +----+----+
    |   |         |
    |   v         v
    | +-------+ +---------+
    | |SUCCESS| |FAILED   |
    | +-------+ +----+----+
    |                |
    |                v  (if retries remaining)
    |           +----------+
    +-----------|UP_FOR    |
                |RETRY     |
                +----------+
```

#### BV-BRC Executor Flow

```
BV-BRC Executor:

1. TRANSLATE: Convert GoWe task definition to BV-BRC job submission JSON-RPC call
   - Map task type (bvbrc:GenomeAssembly2) to BV-BRC app name
   - Map task params to BV-BRC job parameters
   - Resolve input references to BV-BRC workspace paths

2. SUBMIT: Call BV-BRC JSON-RPC API
   POST /rpc
   {
       "jsonrpc": "2.0",
       "method": "AppService.start_app",
       "params": ["GenomeAssembly2", {...job_params...}],
       "id": "gowe-task-{task_id}"
   }

3. TRACK: Store BV-BRC job ID
   - Map GoWe task ID <-> BV-BRC job ID in state store

4. POLL: Periodically check BV-BRC job status
   POST /rpc
   {
       "jsonrpc": "2.0",
       "method": "AppService.query_task_summary",
       "params": [],
       "id": "gowe-poll-{task_id}"
   }

5. COMPLETE: When BV-BRC reports completion
   - Retrieve output file locations from BV-BRC workspace
   - Update GoWe task outputs with actual paths
   - Report success/failure to scheduler
```

### 6.5 How to Bridge Workflow Execution with BV-BRC Job Submission

The bridge between GoWe and BV-BRC requires:

#### 1. BV-BRC Service Registry
Maintain a registry of BV-BRC services with their parameter schemas:

```json
{
    "services": {
        "GenomeAssembly2": {
            "app_id": "GenomeAssembly2",
            "description": "Genome assembly from reads",
            "params_schema": {
                "required": ["output_path", "output_file"],
                "optional": ["recipe", "min_contig_length", "trim"],
                "input_types": ["paired_end_libs", "single_end_libs", "srr_ids"]
            }
        },
        "GenomeAnnotation": {
            "app_id": "GenomeAnnotation",
            "description": "Annotate assembled genome",
            "params_schema": {
                "required": ["scientific_name", "taxonomy_id", "genetic_code", "domain", "output_path", "output_file"],
                "input_types": ["contigs"]
            }
        }
    }
}
```

#### 2. Parameter Translation Layer
A mapping layer that translates GoWe task parameters to BV-BRC JSON-RPC parameters:

- **Direct mapping**: Most parameters pass through directly (output_path, recipe, etc.).
- **Input resolution**: GoWe resolves `{{tasks.assembly.outputs.contigs}}` to actual BV-BRC workspace paths.
- **Authentication injection**: GoWe adds BV-BRC auth tokens to API calls.

#### 3. Status Mapping

| BV-BRC Status | GoWe Task State |
|---------------|-----------------|
| `submitted` | `RUNNING` |
| `queued` | `RUNNING` |
| `in-progress` | `RUNNING` |
| `completed` | `SUCCESS` |
| `failed` | `FAILED` |
| `deleted` | `FAILED` |

#### 4. Output Resolution
After BV-BRC job completion, GoWe must:
- Query the BV-BRC workspace to find output files.
- Map output files to the declared outputs in the workflow definition.
- Make resolved output paths available to downstream tasks.

### 6.6 MVP Feature Set

Based on the analysis of all five engines, here is the recommended **Minimum Viable Product** feature set for GoWe, ordered by implementation priority:

#### Phase 1: Core Engine (Weeks 1-4)

1. **Workflow Definition Parser**
   - JSON workflow definition parsing and validation.
   - JSON Schema-based validation of workflow structure.
   - DAG construction from task dependency declarations.
   - Cycle detection.

2. **State Store**
   - SQLite-backed persistence for workflow and task state.
   - Workflow CRUD operations.
   - Task state machine implementation.

3. **Scheduler**
   - Basic scheduler loop (scan, evaluate, dispatch, poll).
   - Sequential task execution based on dependency order.
   - Simple retry logic (fixed count, no backoff).

4. **Local Executor**
   - Execute tasks as local shell commands.
   - Capture stdout/stderr.
   - Report exit codes.

5. **CLI**
   - `gowe submit <workflow.json>` -- submit a workflow.
   - `gowe status <workflow-id>` -- check workflow status.
   - `gowe list` -- list all workflows.
   - `gowe logs <task-id>` -- view task logs.

#### Phase 2: BV-BRC Integration (Weeks 5-8)

6. **BV-BRC JSON-RPC Client**
   - Authentication (token-based).
   - `start_app` call for job submission.
   - `query_task_summary` for status polling.
   - Workspace API for file/output resolution.

7. **BV-BRC Executor**
   - Translate GoWe tasks to BV-BRC job submissions.
   - Poll BV-BRC for job completion.
   - Resolve output paths from BV-BRC workspace.

8. **REST API**
   - `POST /api/v1/workflows` -- submit workflow.
   - `GET /api/v1/workflows` -- list workflows.
   - `GET /api/v1/workflows/{id}` -- get workflow status.
   - `GET /api/v1/workflows/{id}/tasks` -- get task statuses.
   - `GET /api/v1/workflows/{id}/tasks/{id}/logs` -- get task logs.
   - `PUT /api/v1/workflows/{id}/cancel` -- cancel workflow.
   - `PUT /api/v1/workflows/{id}/retry` -- retry failed workflow.

9. **Template Resolution**
   - Resolve `{{...}}` expressions in workflow definitions.
   - Workflow parameter substitution.
   - Inter-task output reference resolution.

#### Phase 3: Production Features (Weeks 9-12)

10. **Enhanced Retry Logic**
    - Exponential backoff.
    - Per-task retry configuration.
    - Workflow-level error strategies (terminate, finish-running, ignore).

11. **Monitoring & Reporting**
    - Task execution metrics (duration, resource usage).
    - Workflow timeline generation.
    - Status event streaming (WebSocket or SSE).

12. **BV-BRC Service Registry**
    - Typed BV-BRC operators with parameter validation.
    - Auto-discovery of available BV-BRC services.

13. **Workflow Parameterization**
    - YAML config files for workflow parameters.
    - CLI parameter overrides.
    - Default value resolution.

#### Phase 4: Advanced Features (Weeks 13+)

14. **Batch/Wildcard Execution** (inspired by Snakemake)
    - Run the same workflow across multiple input sets.
    - Scatter-gather patterns.

15. **Caching/Resume** (inspired by Nextflow)
    - Hash-based result caching.
    - Resume failed workflows from last checkpoint.

16. **Web UI** (inspired by Airflow)
    - Workflow list and status dashboard.
    - DAG visualization.
    - Task log viewer.
    - Manual trigger/retry controls.

17. **Container Support** (inspired by Nextflow)
    - Per-task container specification.
    - Docker and Singularity executor integration.

18. **Notification System** (inspired by Airflow)
    - Email notifications on workflow completion/failure.
    - Webhook integrations.

---

## Appendix A: Key References

### Nextflow
- Documentation: https://www.nextflow.io/docs/latest/
- GitHub: https://github.com/nextflow-io/nextflow
- Paper: Di Tommaso, P., et al. (2017). "Nextflow enables reproducible computational workflows." Nature Biotechnology, 35(4), 316-319.
- nf-core: https://nf-co.re/

### Snakemake
- Documentation: https://snakemake.readthedocs.io/
- GitHub: https://github.com/snakemake/snakemake
- Paper: Molder, F., et al. (2021). "Sustainable data analysis with Snakemake." F1000Research, 10, 33.
- Workflow Catalog: https://snakemake.github.io/snakemake-workflow-catalog/

### Apache Airflow
- Documentation: https://airflow.apache.org/docs/
- GitHub: https://github.com/apache/airflow
- REST API Reference: https://airflow.apache.org/docs/apache-airflow/stable/stable-rest-api-ref.html
- Provider Packages: https://airflow.apache.org/docs/apache-airflow-providers/

### Parsl
- Documentation: https://parsl.readthedocs.io/
- GitHub: https://github.com/Parsl/parsl
- Paper: Babuji, Y., et al. (2019). "Parsl: Pervasive Parallel Programming in Python." HPDC '19.
- Website: https://parsl-project.org/

### AWE
- GitHub: https://github.com/MG-RAST/AWE
- Related: Shock data management: https://github.com/MG-RAST/Shock
- Paper: Tang, W., et al. (2014). "AWE - A workflow engine for large-scale scientific data analysis." MG-RAST Technical Report.

### BV-BRC
- Website: https://www.bv-brc.org/
- API Documentation: https://www.bv-brc.org/api/
- GitHub: https://github.com/BV-BRC/

---

## Appendix B: Glossary

| Term | Definition |
|------|------------|
| **DAG** | Directed Acyclic Graph -- a graph with directed edges and no cycles, used to represent task dependencies |
| **DSL** | Domain-Specific Language -- a language designed for a specific application domain |
| **Executor** | Component that determines how and where tasks are actually executed |
| **JSON-RPC** | A remote procedure call protocol encoded in JSON |
| **Operator** | (Airflow) A template for a type of task |
| **Process** | (Nextflow) An atomic computational unit in a workflow |
| **Provider** | (Parsl) A component that acquires compute resources from a backend |
| **Rule** | (Snakemake) A transformation from input files to output files |
| **Scheduler** | Component that decides when and where to execute tasks based on dependencies and resources |
| **Shock** | Scientific data management system used with AWE for data storage and provenance |
| **Work Unit** | (AWE) A partition of a task that can be executed independently |
| **Workflow** | A formal definition of a multi-step computational process |
| **XCom** | (Airflow) Cross-communication mechanism for passing data between tasks |
