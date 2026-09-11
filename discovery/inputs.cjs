const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const { pathToFileURL } = require('node:url');

const [root, output] = process.argv.slice(2);
const local = createRequire(path.join(root, 'package.json'));
const result = { production: [], compile: [], suites: {} };

async function discover() {
  const manifest = local('./package.json');
  if (manifest.dependencies?.next || manifest.devDependencies?.next) {
    const loadConfig = local('next/dist/server/config').default;
    const { PHASE_PRODUCTION_BUILD } = local('next/constants');
    const config = await loadConfig(PHASE_PRODUCTION_BUILD, root);
    const { findPagesDir } = local('next/dist/lib/find-pages-dir');
    const { recursiveReadDir } = local('next/dist/lib/recursive-readdir');
    const dirs = findPagesDir(root);
    for (const dir of [dirs.appDir, dirs.pagesDir].filter(Boolean)) {
      const files = await recursiveReadDir(dir);
      result.production.push(...files
        .filter(file => config.pageExtensions.some(ext => file.endsWith('.' + ext)))
        .map(file => path.join(dir, file)));
    }
    const { nodeFileTrace } = local('next/dist/compiled/@vercel/nft');
    const trace = await nodeFileTrace(result.production, { base: root, processCwd: root, ts: true });
    result.production.push(...Array.from(trace.fileList, file => path.resolve(root, file)));
  }
  if (manifest.dependencies?.typescript || manifest.devDependencies?.typescript) {
    const ts = local('typescript');
    const configPath = ts.findConfigFile(root, ts.sys.fileExists);
    if (configPath) {
      const loaded = ts.readConfigFile(configPath, ts.sys.readFile);
      if (loaded.error) throw new Error('typescript configuration');
      const parsed = ts.parseJsonConfigFileContent(loaded.config, ts.sys, path.dirname(configPath));
      if (parsed.errors.length) throw new Error('typescript configuration');
      const program = ts.createProgram(parsed.fileNames, parsed.options);
      result.compile = program.getSourceFiles().map(file => file.fileName);
    }
  }
  for (const suite of ['unit', 'pure', 'integration', 'e2e', 'smoke']) {
    const script = manifest.scripts?.[suite === 'unit' ? 'test' : 'test:' + suite];
    // Shell wrappers and lifecycle hooks cannot be reproduced by runner collection.
    if (!script || !/^vitest(?:\s+run)?$/.test(script.trim())) continue;
    if (manifest.scripts?.['pre' + (suite === 'unit' ? 'test' : 'test:' + suite)]) continue;
    const { createVitest } = await import(pathToFileURL(local.resolve('vitest/node')).href);
    const runner = await createVitest('test', { root, watch: false });
    try {
      const specs = await runner.globTestSpecifications();
      result.suites[suite] = specs.map(spec => spec.moduleId);
    } finally {
      await runner.close();
    }
  }
  fs.writeFileSync(output, JSON.stringify(result));
}

// Configuration and plugin diagnostics may contain secrets; only paths cross back.
discover().catch(() => { process.exitCode = 1; });
