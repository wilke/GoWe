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

// Export for use in templates
window.GoWe = {
  Toast,
  validateForm,
  formatCWL,
  copyToClipboard,
  formatBytes,
  formatDuration,
  initSubmissionPolling
};
