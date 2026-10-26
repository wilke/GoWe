// pm2 config for the alpha production GoWe server. Set GOWE_HOME to the deployment
// directory (bin/, token.key, gowe.db, uploads/, data/, logs/ live there).
const H = process.env.GOWE_HOME || '/path/to/gowe-alpha';

module.exports = {
  apps: [{
    name: 'gowe-server',
    script: `${H}/bin/gowe-server`,
    interpreter: 'none',
    cwd: H,
    args: [
      '--addr', ':12007',
      '--db', `${H}/gowe.db`,
      '--token-key-file', `${H}/token.key`,
      '--allow-anonymous',
      // bvbrc only: local/docker/container anonymously lets anyone who reaches :12007 run code here
      '--anonymous-executors', 'bvbrc',
      '--scheduler-poll', '100ms',
      '--upload-backend', 'local',
      '--upload-local-dir', `${H}/uploads`,
      '--upload-download-dirs', `${H}/uploads,${H}/data`,
      '--workspace-staging', 'server',
      '--log-level', 'info',
      '--admins', 'clark.cucinell,clark.cucinell@patricbrc.org',
      '--record-validation', 'enforce',
      // enforce would refuse 26 of 268 submissions that completed on BV-BRC
      '--input-validation', 'warn',
    ],
    autorestart: true,
    max_restarts: 10,
    restart_delay: 5000,
    kill_timeout: 15000,
    out_file: `${H}/logs/gowe-server.out.log`,
    error_file: `${H}/logs/gowe-server.err.log`,
    time: true,
  }],
};
