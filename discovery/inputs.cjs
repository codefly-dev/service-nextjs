const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const { pathToFileURL } = require('node:url');

const [root, output, task] = process.argv.slice(2);
const local = createRequire(path.join(root, 'package.json'));
const manifest = local('./package.json');

async function discover() {
  const files = new Set();
  if (task === 'TASK_PHASE_ARTIFACT_BUILD' && manifest.scripts?.build === 'next build') {
    const loadConfig = local('next/dist/server/config').default;
    const { PHASE_PRODUCTION_BUILD } = local('next/constants');
    const config = await loadConfig(PHASE_PRODUCTION_BUILD, root);
    const { findPagesDir } = local('next/dist/lib/find-pages-dir');
    const { recursiveReadDir } = local('next/dist/lib/recursive-readdir');
    const dirs = findPagesDir(root);
    const entries = [];
    for (const dir of [dirs.appDir, dirs.pagesDir].filter(Boolean)) {
      for (const file of await recursiveReadDir(dir)) {
        if (config.pageExtensions.some(ext => file.endsWith('.' + ext))) entries.push(path.join(dir, file));
      }
    }
    const { nodeFileTrace } = local('next/dist/compiled/@vercel/nft');
    const ts = local('typescript');
    const trace = await nodeFileTrace(entries, {
      base: '/workspace', processCwd: root, ts: true,
      readFile: async file => {
        let content;
        try { content = await fs.promises.readFile(file, 'utf8'); }
        catch (error) { if (error.code === 'ENOENT' || error.code === 'EISDIR') return null; throw error; }
        if (/\.[jt]sx?$/.test(file) && !file.includes('/node_modules/')) {
          return ts.transpileModule(content, { fileName: file, compilerOptions: { jsx: ts.JsxEmit.React, module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ESNext } }).outputText;
        }
        return content;
      },
    });
    entries.forEach(file => files.add(file));
    trace.fileList.forEach(file => files.add(path.resolve('/workspace', file)));
    for (const name of ['next.config.js', 'next.config.mjs', 'next.config.ts']) {
      if (fs.existsSync(path.join(root, name))) files.add(path.join(root, name));
    }
  } else if (task === 'TASK_PHASE_COMPILE' && manifest.scripts?.typecheck === 'tsc --noEmit') {
    const ts = local('typescript');
    const readFile = file => { files.add(file); return ts.sys.readFile(file); };
    const configPath = ts.findConfigFile(root, ts.sys.fileExists);
    if (!configPath) throw new Error('missing compiler configuration');
    const loaded = ts.readConfigFile(configPath, readFile);
    if (loaded.error) throw new Error('invalid compiler configuration');
    const parsed = ts.parseJsonConfigFileContent(loaded.config, { ...ts.sys, readFile }, path.dirname(configPath));
    if (parsed.errors.length) throw new Error('invalid compiler configuration');
    const host = ts.createCompilerHost(parsed.options);
    host.readFile = readFile;
    ts.createProgram(parsed.fileNames, parsed.options, host).getSourceFiles().forEach(file => files.add(file.fileName));
  } else if (task.startsWith('test/')) {
    const suite = task.slice(5);
    const name = suite === 'unit' ? 'test' : 'test:' + suite;
    const script = manifest.scripts?.[name];
    if (!script || !/^vitest(?:\s+run)?$/.test(script.trim()) || manifest.scripts?.['pre' + name]) throw new Error('unsupported script');
    const { createVitest } = await import(pathToFileURL(local.resolve('vitest/node')).href);
    const runner = await createVitest('test', { root, watch: false }, { configLoader: 'runner', cacheDir: '/tmp/vite' });
    try {
      const specs = await runner.globTestSpecifications();
      for (const spec of specs) {
        files.add(spec.moduleId);
        for (const file of spec.project.config.setupFiles || []) files.add(file);
      }
      for (const file of runner.vite.config.configFileDependencies || []) files.add(file);
      if (runner.vite.config.configFile) files.add(runner.vite.config.configFile);
    } finally { await runner.close(); }
  } else { throw new Error('unsupported operation'); }
  const names = Array.from(files, file => path.relative('/workspace', file)).filter(file => file && !file.startsWith('../') && !path.isAbsolute(file)).sort();
  if (names.length > 512) throw new Error('input budget exceeded');
  fs.writeFileSync(output, JSON.stringify(names));
}

discover().catch(() => { process.exitCode = 1; });
