// GoWe UI JavaScript

// Toast notification system
const Toast = {
  container: null,

  init() {
    this.container = document.createElement('div');
    this.container.id = 'toast-container';
    this.container.className = 'fixed top-4 right-4 z-50 space-y-2';
    document.body.appendChild(this.container);
  },

  show(message, type = 'info', duration = 5000) {
    if (!this.container) this.init();

    const toast = document.createElement('div');
    toast.className = `toast ${type}`;
    toast.innerHTML = `
      <div class="flex items-center">
        <span>${message}</span>
        <button onclick="this.parentElement.parentElement.remove()" class="ml-4 text-gray-400 hover:text-gray-600">
          &times;
        </button>
      </div>
    `;

    this.container.appendChild(toast);

    // Trigger animation
    requestAnimationFrame(() => {
      toast.classList.add('show');
    });

    // Auto-remove after duration
    if (duration > 0) {
      setTimeout(() => {
        toast.classList.remove('show');
        setTimeout(() => toast.remove(), 300);
      }, duration);
    }
  },

  success(message, duration) {
    this.show(message, 'success', duration);
  },

  error(message, duration) {
    this.show(message, 'error', duration);
  },

  info(message, duration) {
    this.show(message, 'info', duration);
  }
};

// HTMX event handlers
document.addEventListener('DOMContentLoaded', function() {
  // Initialize toast container
  Toast.init();

  // Handle HTMX errors
  document.body.addEventListener('htmx:responseError', function(event) {
    Toast.error('Request failed. Please try again.');
  });

  // Handle HTMX swap errors
  document.body.addEventListener('htmx:swapError', function(event) {
    Toast.error('Failed to update page.');
  });

  // Show success toast after successful delete
  document.body.addEventListener('htmx:afterSwap', function(event) {
    const trigger = event.detail.requestConfig.triggeringEvent;
    if (trigger && trigger.target && trigger.target.hasAttribute('hx-delete')) {
      Toast.success('Item deleted successfully.');
    }
  });

  // Confirm dialogs for dangerous actions
  document.body.addEventListener('htmx:confirm', function(event) {
    if (event.detail.question) {
      event.preventDefault();
      if (confirm(event.detail.question)) {
        event.detail.issueRequest();
      }
    }
  });
});

// Submission state auto-refresh
function initSubmissionPolling(submissionId) {
  const container = document.getElementById('submission-container');
  if (!container) return;

  const state = container.dataset.state;
  if (state === 'COMPLETED' || state === 'FAILED' || state === 'CANCELLED') {
    return; // Don't poll for terminal states
  }

  // Use SSE for real-time updates if available
  if (typeof EventSource !== 'undefined') {
    const evtSource = new EventSource(`/api/v1/sse/submissions/${submissionId}`);

    evtSource.addEventListener('update', function(event) {
      const data = JSON.parse(event.data);
      updateSubmissionUI(data);
    });

    evtSource.addEventListener('complete', function(event) {
      evtSource.close();
      location.reload();
    });

    evtSource.onerror = function() {
      evtSource.close();
      // Fall back to polling
      setTimeout(() => location.reload(), 5000);
    };
  }
}

function updateSubmissionUI(submission) {
  // Update state badge
  const badge = document.querySelector('[data-state-badge]');
  if (badge) {
    badge.textContent = submission.state;
    badge.className = badge.className.replace(/badge-\w+/, `badge-${submission.state.toLowerCase()}`);
  }
}

// Form validation helpers
function validateForm(form) {
  const inputs = form.querySelectorAll('input[required], textarea[required], select[required]');
  let valid = true;

  inputs.forEach(input => {
    if (!input.value.trim()) {
      input.classList.add('border-red-500');
      valid = false;
    } else {
      input.classList.remove('border-red-500');
    }
  });

  return valid;
}

// CWL editor helpers
function formatCWL(textarea) {
  try {
    const value = textarea.value;
    // Basic YAML/JSON detection and formatting
    if (value.trim().startsWith('{')) {
      const parsed = JSON.parse(value);
      textarea.value = JSON.stringify(parsed, null, 2);
    }
  } catch (e) {
    // Ignore formatting errors
  }
}

// Utility functions
function copyToClipboard(text) {
  navigator.clipboard.writeText(text).then(() => {
    Toast.success('Copied to clipboard');
  }).catch(() => {
    Toast.error('Failed to copy');
  });
}

function formatBytes(bytes) {
  if (bytes === 0) return '0 Bytes';
  const k = 1024;
  const sizes = ['Bytes', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

function formatDuration(seconds) {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  const hours = Math.floor(seconds / 3600);
  const mins = Math.floor((seconds % 3600) / 60);
  return `${hours}h ${mins}m`;
}

// File Picker for workspace files
const FilePicker = {
  modal: null,
  currentPath: '',
  currentInputId: null,
  onSelect: null,

  init() {
    if (this.modal) return;

    const modalHtml = `
      <div id="file-picker-modal" class="fixed inset-0 z-50 hidden">
        <div class="absolute inset-0 bg-black bg-opacity-50" onclick="GoWe.FilePicker.close()"></div>
        <div class="absolute inset-4 md:inset-10 lg:inset-20 bg-white rounded-lg shadow-xl flex flex-col">
          <div class="px-4 py-3 border-b flex items-center justify-between">
            <h3 class="text-lg font-semibold">Select File from Workspace</h3>
            <button onclick="GoWe.FilePicker.close()" class="text-gray-400 hover:text-gray-600 text-2xl">&times;</button>
          </div>
          <div class="px-4 py-2 bg-gray-50 border-b flex items-center space-x-2">
            <button onclick="GoWe.FilePicker.goUp()" class="px-2 py-1 text-sm bg-gray-200 rounded hover:bg-gray-300">↑ Up</button>
            <span id="file-picker-path" class="text-sm text-gray-600 font-mono truncate"></span>
          </div>
          <div id="file-picker-content" class="flex-1 overflow-auto p-4">
            <div class="text-gray-500 text-center py-8">Loading...</div>
          </div>
          <div id="file-picker-upload" class="px-4 py-3 border-t bg-gray-50">
            <div class="flex items-center space-x-2">
              <span class="text-sm text-gray-600">Or upload a file:</span>
              <input type="file" id="file-picker-upload-input" class="text-sm" onchange="GoWe.FilePicker.uploadFile(this)">
              <span id="file-picker-upload-status" class="text-sm text-gray-500"></span>
            </div>
          </div>
        </div>
      </div>
    `;

    document.body.insertAdjacentHTML('beforeend', modalHtml);
    this.modal = document.getElementById('file-picker-modal');
  },

  open(inputId, initialPath) {
    this.init();
    this.currentInputId = inputId;
    this.currentPath = initialPath || '';
    this.modal.classList.remove('hidden');
    this.loadFolder(this.currentPath);
  },

  close() {
    if (this.modal) {
      this.modal.classList.add('hidden');
    }
  },

  async loadFolder(path) {
    const content = document.getElementById('file-picker-content');
    const pathDisplay = document.getElementById('file-picker-path');

    content.innerHTML = '<div class="text-gray-500 text-center py-8">Loading...</div>';

    try {
      const url = path ? `/api/workspace/ls?path=${encodeURIComponent(path)}` : '/api/workspace/ls';
      const resp = await fetch(url);
      const data = await resp.json();

      if (data.error) {
        content.innerHTML = `<div class="text-red-500 text-center py-8">${data.error}</div>`;
        return;
      }

      this.currentPath = data.path;
      pathDisplay.textContent = data.path;

      if (data.items.length === 0) {
        content.innerHTML = '<div class="text-gray-500 text-center py-8">Empty folder</div>';
        return;
      }

      // Sort: folders first, then files
      data.items.sort((a, b) => {
        if (a.isFolder && !b.isFolder) return -1;
        if (!a.isFolder && b.isFolder) return 1;
        return a.name.localeCompare(b.name);
      });

      let html = '<div class="space-y-1">';
      for (const item of data.items) {
        const icon = item.isFolder ? '📁' : '📄';
        const sizeStr = item.isFolder ? '' : ` (${GoWe.formatBytes(item.size)})`;
        const clickAction = item.isFolder
          ? `GoWe.FilePicker.loadFolder('${item.path}')`
          : `GoWe.FilePicker.selectFile('${item.path}')`;
        const bgClass = item.isFolder ? 'hover:bg-gray-100' : 'hover:bg-blue-50 cursor-pointer';

        html += `
          <div onclick="${clickAction}" class="px-3 py-2 rounded ${bgClass} flex items-center justify-between">
            <span>${icon} ${item.name}</span>
            <span class="text-xs text-gray-400">${item.type}${sizeStr}</span>
          </div>
        `;
      }
      html += '</div>';
      content.innerHTML = html;

    } catch (err) {
      content.innerHTML = `<div class="text-red-500 text-center py-8">Failed to load: ${err.message}</div>`;
    }
  },

  goUp() {
    if (!this.currentPath) return;
    const parts = this.currentPath.split('/').filter(p => p);
    if (parts.length <= 2) return; // Don't go above /user/home
    parts.pop();
    this.loadFolder('/' + parts.join('/'));
  },

  selectFile(path) {
    if (this.currentInputId) {
      const input = document.getElementById(this.currentInputId);
      if (input) {
        input.value = path;
        // Trigger change event for any listeners
        input.dispatchEvent(new Event('change'));
      }
    }
    this.close();
    Toast.success(`Selected: ${path.split('/').pop()}`);
  },

  async uploadFile(fileInput) {
    const file = fileInput.files[0];
    if (!file) return;

    const status = document.getElementById('file-picker-upload-status');
    status.textContent = 'Uploading...';
    status.className = 'text-sm text-blue-500';

    const formData = new FormData();
    formData.append('file', file);
    formData.append('folder', this.currentPath);

    try {
      const resp = await fetch('/api/workspace/upload', {
        method: 'POST',
        body: formData
      });

      const data = await resp.json();

      if (data.error) {
        status.textContent = data.error;
        status.className = 'text-sm text-red-500';
        return;
      }

      status.textContent = 'Uploaded!';
      status.className = 'text-sm text-green-500';

      // Select the uploaded file
      this.selectFile(data.path);

      // Reset file input
      fileInput.value = '';

    } catch (err) {
      status.textContent = `Failed: ${err.message}`;
      status.className = 'text-sm text-red-500';
    }
  }
};

// Export for use in templates
window.GoWe = {
  Toast,
  validateForm,
  formatCWL,
  copyToClipboard,
  formatBytes,
  formatDuration,
  initSubmissionPolling,
  FilePicker
};
