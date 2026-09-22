const test = require('node:test');
const assert = require('node:assert/strict');

test('Init supplies the accepted dependency endpoint and invocation inputs', async () => {
  const address = process.env.CODEFLY__ENDPOINT__MOD__API__HTTP__HTTP;
  assert.ok(address, 'dependency endpoint must be injected');
  const response = await fetch(address);
  assert.equal(await response.text(), 'accepted dependency');
  assert.equal(process.env.CODEFLY__FIXTURE, 'dev-admin');
  assert.equal(process.env.TEST_INVOCATION_INPUT, 'from-init');
});
