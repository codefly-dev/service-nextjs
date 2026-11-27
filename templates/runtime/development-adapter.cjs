const path = require('node:path');
const { createRequire } = require('node:module');
const { pathToFileURL } = require('node:url');

module.exports = {
  name: 'codefly-scoped-development',
  async modifyConfig(config, context) {
    if (context.phase !== 'phase-development-server') return config;
    const prior = process.env.CODEFLY_NEXT_DELEGATE_ADAPTER;
    if (prior && prior !== __filename) {
      const resolve = createRequire(path.join(context.projectDir, 'package.json')).resolve;
      const imported = await import(pathToFileURL(resolve(prior)).href);
      const adapter = imported.default ?? imported;
      if (adapter.modifyConfig) config = await adapter.modifyConfig(config, context);
    }
    // Next has already resolved async configs, applied defaults and selected
    // the development subdirectory. Keep the user's root and Next's layout.
    const root = config.distDirRoot ?? config.distDir;
    const scopeRoot = path.join(root, 'codefly', process.env.CODEFLY_NEXT_OUTPUT_SCOPE);
    return {
      ...config,
      distDirRoot: scopeRoot,
      distDir: path.join(scopeRoot, path.relative(root, config.distDir)),
    };
  },
};
