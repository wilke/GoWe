// DAG Editor Vue Component
// Provides visual workflow step visualization with interactive features

const DagEditor = {
  props: {
    steps: {
      type: Array,
      default: () => []
    },
    tasks: {
      type: Array,
      default: () => []
    },
    readonly: {
      type: Boolean,
      default: true
    }
  },

  data() {
    return {
      nodes: [],
      edges: [],
      selectedNode: null,
      svgWidth: 800,
      svgHeight: 400,
      nodeWidth: 160,
      nodeHeight: 50,
      levelGap: 100,
      nodeGap: 30,
      isDragging: false,
      dragOffset: { x: 0, y: 0 }
    };
  },

  computed: {
    viewBox() {
      return `0 0 ${this.svgWidth} ${this.svgHeight}`;
    }
  },

  watch: {
    steps: {
      immediate: true,
      handler() {
        this.buildGraph();
      }
    },
    tasks: {
      immediate: true,
      handler() {
        this.updateTaskStates();
      }
    }
  },

  methods: {
    buildGraph() {
      if (!this.steps || this.steps.length === 0) {
        this.nodes = [];
        this.edges = [];
        return;
      }

      // Create nodes from steps
      const nodeMap = new Map();
      this.steps.forEach((step, index) => {
        const node = {
          id: step.id || step.ID || `step-${index}`,
          label: step.id || step.ID || `Step ${index + 1}`,
          type: this.getStepType(step),
          toolClass: this.getToolClass(step),
          bvbrcApp: this.getBvbrcApp(step),
          executor: this.getExecutor(step),
          inputs: step.in || [],
          outputs: step.out || [],
          state: null, // null means no state (workflow definition view)
          x: 0,
          y: 0,
          level: 0
        };
        nodeMap.set(node.id, node);
      });

      // Build edges from input references
      const edges = [];
      this.steps.forEach(step => {
        const targetId = step.id || step.ID;
        const inputs = step.in || [];

        inputs.forEach(input => {
          // Parse source reference (e.g., "step1/output" or just "step1")
          let source = null;
          if (typeof input === 'object' && input.source) {
            const parts = input.source.split('/');
            if (parts.length >= 1 && nodeMap.has(parts[0])) {
              source = parts[0];
            }
          } else if (typeof input === 'string') {
            const parts = input.split('/');
            if (parts.length >= 1 && nodeMap.has(parts[0])) {
              source = parts[0];
            }
          }

          if (source && source !== targetId) {
            edges.push({
              id: `${source}-${targetId}`,
              source: source,
              target: targetId
            });
          }
        });
      });

      // Calculate levels using topological sort
      this.calculateLevels(nodeMap, edges);

      // Position nodes
      this.positionNodes(nodeMap);

      this.nodes = Array.from(nodeMap.values());
      this.edges = edges;

      // Update SVG dimensions based on content
      this.updateDimensions();
    },

    getStepType(step) {
      // Check for BV-BRC app first
      const bvbrcApp = this.getBvbrcApp(step);
      if (bvbrcApp) return 'BV-BRC App';

      // Check tool_inline or run field
      const toolInline = step.tool_inline || step.toolInline;
      if (toolInline) {
        const cls = toolInline.class || toolInline.Class;
        if (cls === 'CommandLineTool') return 'CommandLineTool';
        if (cls === 'Workflow') return 'Workflow';
        if (cls === 'ExpressionTool') return 'ExpressionTool';
        return cls || 'Tool';
      }

      // Check run field (CWL reference)
      const run = step.run;
      if (typeof run === 'string') {
        if (run.startsWith('#')) return 'Tool Reference';
        if (run.endsWith('.cwl')) return 'CWL File';
        return 'Tool';
      }
      if (typeof run === 'object') {
        return run.class || 'Tool';
      }

      // Check tool_ref
      if (step.tool_ref || step.toolRef) {
        return 'Tool Reference';
      }

      return 'Step';
    },

    getToolClass(step) {
      const toolInline = step.tool_inline || step.toolInline;
      if (toolInline) {
        return toolInline.class || toolInline.Class || null;
      }
      return null;
    },

    getBvbrcApp(step) {
      // Check hints at step level
      const hints = step.hints || step.Hints;
      if (hints) {
        if (hints.bvbrc_app_id) return hints.bvbrc_app_id;
        if (hints.BvbrcAppID) return hints.BvbrcAppID;
      }

      // Check hints in tool_inline
      const toolInline = step.tool_inline || step.toolInline;
      if (toolInline && toolInline.hints) {
        if (toolInline.hints.bvbrc_app_id) return toolInline.hints.bvbrc_app_id;
        if (toolInline.hints.BvbrcAppID) return toolInline.hints.BvbrcAppID;
      }

      return null;
    },

    getExecutor(step) {
      const hints = step.hints || step.Hints;
      if (hints) {
        if (hints.executor) return hints.executor;
        if (hints.Executor) return hints.Executor;
      }

      const toolInline = step.tool_inline || step.toolInline;
      if (toolInline && toolInline.hints) {
        if (toolInline.hints.executor) return toolInline.hints.executor;
      }

      return null;
    },

    calculateLevels(nodeMap, edges) {
      // Build adjacency list
      const inDegree = new Map();
      const outEdges = new Map();

      nodeMap.forEach((node, id) => {
        inDegree.set(id, 0);
        outEdges.set(id, []);
      });

      edges.forEach(edge => {
        inDegree.set(edge.target, (inDegree.get(edge.target) || 0) + 1);
        outEdges.get(edge.source)?.push(edge.target);
      });

      // Topological sort with level assignment
      const queue = [];
      nodeMap.forEach((node, id) => {
        if (inDegree.get(id) === 0) {
          queue.push(id);
          node.level = 0;
        }
      });

      while (queue.length > 0) {
        const current = queue.shift();
        const currentNode = nodeMap.get(current);
        const currentLevel = currentNode.level;

        outEdges.get(current)?.forEach(targetId => {
          const targetNode = nodeMap.get(targetId);
          targetNode.level = Math.max(targetNode.level, currentLevel + 1);

          const newInDegree = inDegree.get(targetId) - 1;
          inDegree.set(targetId, newInDegree);

          if (newInDegree === 0) {
            queue.push(targetId);
          }
        });
      }
    },

    positionNodes(nodeMap) {
      // Group nodes by level
      const levels = new Map();
      nodeMap.forEach(node => {
        if (!levels.has(node.level)) {
          levels.set(node.level, []);
        }
        levels.get(node.level).push(node);
      });

      // Position each level
      const startX = 50;
      const startY = 50;

      levels.forEach((nodes, level) => {
        const x = startX + level * (this.nodeWidth + this.levelGap);
        const totalHeight = nodes.length * this.nodeHeight + (nodes.length - 1) * this.nodeGap;
        let y = startY + (this.svgHeight - totalHeight) / 2;

        nodes.forEach(node => {
          node.x = x;
          node.y = Math.max(startY, y);
          y += this.nodeHeight + this.nodeGap;
        });
      });
    },

    updateDimensions() {
      let maxX = 0;
      let maxY = 0;

      this.nodes.forEach(node => {
        maxX = Math.max(maxX, node.x + this.nodeWidth + 50);
        maxY = Math.max(maxY, node.y + this.nodeHeight + 50);
      });

      this.svgWidth = Math.max(800, maxX);
      this.svgHeight = Math.max(300, maxY);
    },

    updateTaskStates() {
      if (!this.tasks || this.tasks.length === 0) return;

      // Create a map of step ID to task state
      const taskStates = new Map();
      this.tasks.forEach(task => {
        const stepId = task.step_id || task.StepID;
        const state = task.state || task.State;
        if (stepId) {
          taskStates.set(stepId, state?.toLowerCase() || 'pending');
        }
      });

      // Update node states
      this.nodes.forEach(node => {
        if (taskStates.has(node.id)) {
          node.state = taskStates.get(node.id);
        }
      });
    },

    getEdgePath(edge) {
      const source = this.nodes.find(n => n.id === edge.source);
      const target = this.nodes.find(n => n.id === edge.target);

      if (!source || !target) return '';

      const x1 = source.x + this.nodeWidth;
      const y1 = source.y + this.nodeHeight / 2;
      const x2 = target.x;
      const y2 = target.y + this.nodeHeight / 2;

      // Bezier curve for smooth edges
      const midX = (x1 + x2) / 2;
      return `M ${x1} ${y1} C ${midX} ${y1}, ${midX} ${y2}, ${x2} ${y2}`;
    },

    getNodeClass(node) {
      const classes = ['dag-node'];
      if (node.state) {
        classes.push(`dag-node-${node.state}`);
      }
      if (this.selectedNode === node.id) {
        classes.push('dag-node-selected');
      }
      return classes.join(' ');
    },

    getNodeColor(node) {
      // No state = workflow definition view, use neutral color based on type
      if (!node.state) {
        if (node.bvbrcApp) return '#6366f1'; // Indigo for BV-BRC apps
        return '#8b5cf6'; // Purple for other tools
      }

      switch (node.state) {
        case 'success':
        case 'completed':
          return '#22c55e';
        case 'running':
          return '#3b82f6';
        case 'failed':
          return '#ef4444';
        case 'queued':
        case 'scheduled':
          return '#f59e0b';
        case 'skipped':
          return '#9ca3af';
        case 'pending':
          return '#e5e7eb';
        default:
          return '#e5e7eb';
      }
    },

    getNodeTextColor(node) {
      // No state = workflow definition view
      if (!node.state) {
        return '#ffffff';
      }

      switch (node.state) {
        case 'success':
        case 'completed':
        case 'running':
        case 'failed':
          return '#ffffff';
        default:
          return '#374151';
      }
    },

    selectNode(node) {
      this.selectedNode = this.selectedNode === node.id ? null : node.id;
      this.$emit('node-selected', node);
    },

    onMouseDown(event, node) {
      if (this.readonly) return;

      this.isDragging = true;
      this.selectedNode = node.id;
      this.dragOffset = {
        x: event.clientX - node.x,
        y: event.clientY - node.y
      };

      document.addEventListener('mousemove', this.onMouseMove);
      document.addEventListener('mouseup', this.onMouseUp);
    },

    onMouseMove(event) {
      if (!this.isDragging || !this.selectedNode) return;

      const node = this.nodes.find(n => n.id === this.selectedNode);
      if (node) {
        node.x = Math.max(10, event.clientX - this.dragOffset.x);
        node.y = Math.max(10, event.clientY - this.dragOffset.y);
      }
    },

    onMouseUp() {
      this.isDragging = false;
      document.removeEventListener('mousemove', this.onMouseMove);
      document.removeEventListener('mouseup', this.onMouseUp);
    },

    zoomIn() {
      this.nodeWidth = Math.min(250, this.nodeWidth * 1.1);
      this.nodeHeight = Math.min(80, this.nodeHeight * 1.1);
      this.buildGraph();
    },

    zoomOut() {
      this.nodeWidth = Math.max(100, this.nodeWidth * 0.9);
      this.nodeHeight = Math.max(30, this.nodeHeight * 0.9);
      this.buildGraph();
    },

    resetView() {
      this.nodeWidth = 160;
      this.nodeHeight = 50;
      this.buildGraph();
    }
  },

  template: `
    <div class="dag-editor">
      <div class="dag-toolbar">
        <button @click="zoomIn" class="dag-btn" title="Zoom In">
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0zM10 7v3m0 0v3m0-3h3m-3 0H7" />
          </svg>
        </button>
        <button @click="zoomOut" class="dag-btn" title="Zoom Out">
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0zM13 10H7" />
          </svg>
        </button>
        <button @click="resetView" class="dag-btn" title="Reset View">
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
          </svg>
        </button>
        <span class="dag-info">{{ nodes.length }} steps</span>
      </div>

      <div class="dag-canvas">
        <svg :viewBox="viewBox" :width="svgWidth" :height="svgHeight" class="dag-svg">
          <defs>
            <marker id="arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto">
              <polygon points="0 0, 10 3.5, 0 7" fill="#9ca3af" />
            </marker>
          </defs>

          <!-- Edges -->
          <g class="dag-edges">
            <path
              v-for="edge in edges"
              :key="edge.id"
              :d="getEdgePath(edge)"
              fill="none"
              stroke="#9ca3af"
              stroke-width="2"
              marker-end="url(#arrowhead)"
              class="dag-edge"
            />
          </g>

          <!-- Nodes -->
          <g class="dag-nodes">
            <g
              v-for="node in nodes"
              :key="node.id"
              :transform="'translate(' + node.x + ',' + node.y + ')'"
              :class="getNodeClass(node)"
              @click="selectNode(node)"
              @mousedown="onMouseDown($event, node)"
              style="cursor: pointer;"
            >
              <rect
                :width="nodeWidth"
                :height="nodeHeight"
                rx="6"
                ry="6"
                :fill="getNodeColor(node)"
                stroke="#d1d5db"
                stroke-width="1"
              />
              <text
                :x="nodeWidth / 2"
                :y="nodeHeight / 2"
                text-anchor="middle"
                dominant-baseline="middle"
                :fill="getNodeTextColor(node)"
                font-size="12"
                font-weight="500"
              >
                {{ node.label.length > 18 ? node.label.substring(0, 18) + '...' : node.label }}
              </text>

              <!-- State indicator -->
              <circle
                v-if="node.state === 'running'"
                :cx="nodeWidth - 12"
                cy="12"
                r="4"
                fill="#ffffff"
                class="dag-pulse"
              />
            </g>
          </g>
        </svg>
      </div>

      <!-- Node details panel -->
      <div v-if="selectedNode" class="dag-details">
        <div class="dag-details-header">
          <h4>{{ nodes.find(n => n.id === selectedNode)?.label }}</h4>
          <button @click="selectedNode = null" class="dag-close">&times;</button>
        </div>
        <div class="dag-details-content">
          <p><strong>Type:</strong> {{ nodes.find(n => n.id === selectedNode)?.type }}</p>
          <p v-if="nodes.find(n => n.id === selectedNode)?.bvbrcApp">
            <strong>BV-BRC App:</strong> {{ nodes.find(n => n.id === selectedNode)?.bvbrcApp }}
          </p>
          <p v-if="nodes.find(n => n.id === selectedNode)?.executor">
            <strong>Executor:</strong> {{ nodes.find(n => n.id === selectedNode)?.executor }}
          </p>
          <p v-if="nodes.find(n => n.id === selectedNode)?.state">
            <strong>State:</strong> {{ nodes.find(n => n.id === selectedNode)?.state }}
          </p>
        </div>
      </div>
    </div>
  `
};

// CSS styles for the DAG editor
const dagStyles = `
.dag-editor {
  position: relative;
  border: 1px solid #e5e7eb;
  border-radius: 0.5rem;
  background: #f9fafb;
  overflow: hidden;
}

.dag-toolbar {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem;
  background: white;
  border-bottom: 1px solid #e5e7eb;
}

.dag-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  border: 1px solid #d1d5db;
  border-radius: 0.375rem;
  background: white;
  cursor: pointer;
  transition: background 0.15s;
}

.dag-btn:hover {
  background: #f3f4f6;
}

.dag-info {
  margin-left: auto;
  font-size: 0.75rem;
  color: #6b7280;
}

.dag-canvas {
  overflow: auto;
  padding: 1rem;
  min-height: 300px;
  max-height: 500px;
}

.dag-svg {
  display: block;
}

.dag-edge {
  transition: stroke 0.15s;
}

.dag-node {
  transition: transform 0.1s;
}

.dag-node:hover rect {
  filter: brightness(0.95);
}

.dag-node-selected rect {
  stroke: #4f46e5;
  stroke-width: 2;
}

.dag-pulse {
  animation: pulse 1.5s ease-in-out infinite;
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.3; }
}

.dag-details {
  position: absolute;
  bottom: 0;
  right: 0;
  width: 250px;
  background: white;
  border-top: 1px solid #e5e7eb;
  border-left: 1px solid #e5e7eb;
  border-radius: 0.5rem 0 0 0;
  box-shadow: -2px -2px 8px rgba(0,0,0,0.05);
}

.dag-details-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.5rem 0.75rem;
  background: #f9fafb;
  border-bottom: 1px solid #e5e7eb;
}

.dag-details-header h4 {
  margin: 0;
  font-size: 0.875rem;
  font-weight: 600;
}

.dag-close {
  border: none;
  background: none;
  font-size: 1.25rem;
  cursor: pointer;
  color: #6b7280;
}

.dag-details-content {
  padding: 0.75rem;
  font-size: 0.75rem;
}

.dag-details-content p {
  margin: 0.25rem 0;
}
`;

// Inject styles
if (typeof document !== 'undefined') {
  const styleEl = document.createElement('style');
  styleEl.textContent = dagStyles;
  document.head.appendChild(styleEl);
}

// Export for use
if (typeof window !== 'undefined') {
  window.DagEditor = DagEditor;
}
