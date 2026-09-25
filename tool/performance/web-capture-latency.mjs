import assert from 'node:assert/strict';
import http from 'node:http';
import tls from 'node:tls';
import { randomBytes } from 'node:crypto';
import { execFile, spawn } from 'node:child_process';
import { cp, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createInterface } from 'node:readline';
import { promisify } from 'node:util';
import { createRequire } from 'node:module';
import { performance } from 'node:perf_hooks';
import { fileURLToPath } from 'node:url';

const { chromium } = createRequire(import.meta.url)(
  process.env.VIBERMATE_PLAYWRIGHT_MODULE ?? 'playwright',
);
const execute = promisify(execFile);
const root = fileURLToPath(new URL('../../', import.meta.url));
const fixture = await mkdtemp(join(tmpdir(), 'vm-perf-web-'));
const binary = join(fixture, 'vibermated');
const data = join(fixture, 'data');
let child;
let provider;
let browser;

const headers = (origin, token, revision = 0) => ({
  Origin: origin,
  Authorization: 'Bearer ' + token,
  'Content-Type': 'application/json',
  'If-Match': String(revision),
  'Idempotency-Key': randomBytes(24).toString('hex'),
});

async function json(response, expected, label) {
  const value = await response.json();
  if (response.status !== expected) {
    throw new Error(label + ': HTTP ' + response.status + ' ' + (value.code ?? 'unexpected_response'));
  }
  return value;
}

async function send(grant, rootPEM, marker, expectSuccess = true) {
  const proxy = new URL(grant.proxyAddress);
  const credential = Buffer.from(grant.proxyUsername + ':' + grant.proxyPassword).toString('base64');
  const tunnel = await new Promise((resolve, reject) => {
    const request = http.request({
      hostname: proxy.hostname, port: proxy.port,
      method: 'CONNECT', path: 'api.anthropic.com:443',
      headers: { 'Proxy-Authorization': 'Basic ' + credential },
    });
    request.once('connect', (response, socket) => {
      if (response.statusCode !== 200) {
        socket.destroy();
        reject(new Error('CONNECT HTTP ' + response.statusCode));
      } else resolve(socket);
    });
    request.once('error', reject);
    request.end();
  });
  const secure = tls.connect({ socket: tunnel, servername: 'api.anthropic.com',
    ca: rootPEM, rejectUnauthorized: true });
  await new Promise((resolve, reject) => {
    secure.once('secureConnect', resolve);
    secure.once('error', reject);
  });
  const body = JSON.stringify({ model: 'claude-sonnet-4-5', max_tokens: 16,
    messages: [{ role: 'user', content: marker }] });
  const response = await new Promise((resolve, reject) => {
    const chunks = [];
    secure.setTimeout(15_000, () => secure.destroy(new Error('provider timeout')));
    secure.on('data', chunk => chunks.push(chunk));
    secure.once('end', () => resolve(Buffer.concat(chunks).toString()));
    secure.once('error', reject);
    secure.write('POST /v1/messages HTTP/1.1\r\n' +
      'Host: api.anthropic.com\r\n' +
      'Content-Type: application/json\r\n' +
      'Anthropic-Version: 2023-06-01\r\n' +
      'X-Api-Key: synthetic-client-key\r\n' +
      'Content-Length: ' + Buffer.byteLength(body) + '\r\n' +
      'Connection: close\r\n\r\n' + body);
  });
  if (expectSuccess) assert.ok(response.startsWith('HTTP/1.1 200 OK'));
  return response;
}

const percentile = (values, fraction) => {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.ceil(sorted.length * fraction) - 1];
};
const stats = (values, field) => {
  const samples = values.map(item => item[field]).filter(value => value != null);
  return { samples: samples.length,
    p50: samples.length ? percentile(samples, 0.5) : null,
    p95: samples.length ? percentile(samples, 0.95) : null };
};

try {
  provider = http.createServer(async (request, response) => {
    for await (const _ of request) {}
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify({
      id: 'msg_perf', type: 'message', role: 'assistant',
      content: [{ type: 'text', text: 'SYNTHETIC_PROVIDER_OK' }],
      model: 'claude-sonnet-4-5', stop_reason: 'end_turn',
      stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 },
    }));
  });
  await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve));
  const providerOrigin = 'http://127.0.0.1:' + provider.address().port;
  await execute('go', ['build', '-o', binary, './cmd/vibermated'], {
    cwd: root, timeout: 180_000,
  });
  await cp(join(root, 'ui/flutter_app/build/web'), join(fixture, 'vibermate-web'), {
    recursive: true,
  });
  child = spawn(binary, ['server', '--data-dir', data, '--listen', '127.0.0.1:0'], {
    cwd: fixture, stdio: ['ignore', 'pipe', 'pipe'],
  });
  child.stderr.resume();
  const lines = createInterface({ input: child.stdout });
  const status = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('server startup timeout')), 30_000);
    lines.once('line', line => {
      clearTimeout(timeout);
      try { resolve(JSON.parse(line)); } catch (error) { reject(error); }
    });
    child.once('exit', code => reject(new Error('server exited ' + code)));
  });
  lines.close();
  child.stdout.resume();
  assert.equal(status.ready, true);
  const origin = 'http://' + status.listenAddress;
  const recoveryKey = (await execute(binary, ['server', 'recovery-key', '--data-dir', data])).stdout.trim();
  const password = 'fixture-' + randomBytes(24).toString('hex');
  const owner = await json(await fetch(origin + '/api/v1/server/web-setup', {
    method: 'POST', headers: { Origin: origin, 'Content-Type': 'application/json' },
    body: JSON.stringify({ schema: 'vibermate-web-setup-v1',
      recoveryKey, username: 'playwright-owner', password }),
  }), 201, 'owner setup');
  const endpoint = await json(await fetch(origin + '/api/v1/upstream-endpoints', {
    method: 'POST', headers: headers(origin, owner.writeToken),
    body: JSON.stringify({ id: 'target.perf', displayName: 'Perf provider',
      origin: providerOrigin, backendProtocols: ['anthropic_messages'] }),
  }), 201, 'endpoint');
  const account = await json(await fetch(origin + '/api/v1/provider-accounts', {
    method: 'POST', headers: headers(origin, owner.writeToken),
    body: JSON.stringify({ id: 'account.perf', displayName: 'Perf account',
      upstreamEndpointId: endpoint.id, kind: 'anthropic_api_key',
      secret: 'synthetic-test-token-0123456789abcdef',
      setHeaders: {}, deleteHeaders: [] }),
  }), 201, 'account');
  const environmentId = 'fixture.perf';
  const routeId = 'route.perf';
  const candidate = {
    expectedDraftRevision: 0, name: 'Perf fixture', state: 'active',
    clientEndpoints: [{ id: 'endpoint.perf', revision: 1,
      clientOrigin: 'https://api.anthropic.com', protocolPlans: [{
        id: 'plan.perf', revision: 1, clientProtocol: 'anthropic_messages',
        clientAdapterPolicy: { id: 'adapter.claude', revision: 1 },
        destination: { kind: 'upstream', upstream: {
          routes: [{ id: routeId, revision: 1,
            providerTarget: { id: endpoint.id, revision: endpoint.revision,
              origin: endpoint.origin, realmId: endpoint.realmId,
              capabilities: endpoint.capabilities },
            backendProtocol: 'anthropic_messages',
            accountPolicy: { revision: 1, mode: 'fixed', fixedAccountId: account.id,
              accounts: [{ id: account.id, revision: account.revision,
                displayName: account.displayName }] },
            modelPolicy: { revision: 1, mode: 'passthrough', mappings: [] },
            wireProfileRef: 'follow-client', pluginBindings: [] }],
          defaultRouteId: routeId,
          routeSet: { id: 'routes.perf', revision: 1,
            candidateRouteIds: [routeId] } } },
        egressProfile: { id: 'profile.direct', revision: 1,
          displayName: 'Direct · System DNS',
          policy: { proxy: { kind: 'direct' },
            resolver: { kind: 'system', transport: 'direct' } },
          publishedAt: '1970-01-01T00:00:00.000Z' },
        transforms: [], pluginBindings: [] }] }],
    pluginBindings: [], budgetPolicy: { id: '', revision: 0 },
    contentRecording: { mode: 'full', retentionDays: 30 },
    launchEnvironment: {}, policySet: { toolMode: 'observe' },
  };
  const draftURL = origin + '/api/v1/environments/' + environmentId + '/draft';
  const draft = await json(await fetch(draftURL, {
    method: 'PUT', headers: headers(origin, owner.writeToken),
    body: JSON.stringify(candidate),
  }), 200, 'draft');
  await json(await fetch(draftURL + '/actions/preview', {
    method: 'POST', headers: headers(origin, owner.writeToken, draft.draftRevision),
  }), 200, 'preview');
  await json(await fetch(draftURL + '/actions/publish', {
    method: 'POST', headers: headers(origin, owner.writeToken, draft.draftRevision),
  }), 200, 'publish');
  const revised = structuredClone(candidate);
  const revisedEndpoint = revised.clientEndpoints[0];
  const revisedPlan = revisedEndpoint.protocolPlans[0];
  const revisedRoute = revisedPlan.destination.upstream.routes[0];
  revisedEndpoint.revision++;
  revisedPlan.revision++;
  revisedRoute.revision++;
  revisedRoute.modelPolicy = { revision: 2, mode: 'map', mappings: [
    { requestedModel: 'example-model', upstreamModel: 'synthetic-draft-model' },
  ] };
  const unpublished = await json(await fetch(draftURL, {
    method: 'PUT', headers: headers(origin, owner.writeToken, 1),
    body: JSON.stringify(revised),
  }), 200, 'unpublished draft');
  const rootCA = await json(await fetch(origin + '/api/v1/server/root-ca', {
    headers: { Origin: origin, Authorization: 'Bearer ' + owner.readToken },
  }), 200, 'root CA');
  browser = await chromium.launch({
    ...(process.env.VIBERMATE_BROWSER_CHANNEL
      ? { channel: process.env.VIBERMATE_BROWSER_CHANNEL } : {}),
    headless: true,
  });

  const trialPage = await browser.newPage({ viewport: { width: 390, height: 760 } });
  await trialPage.goto(origin);
  await trialPage.locator('input#username').waitFor();
  await trialPage.locator('input#username').fill('playwright-owner');
  await trialPage.mouse.click(150, 527);
  await trialPage.keyboard.type(password);
  const trialLogin = trialPage.waitForResponse(response =>
    response.url().endsWith('/api/v1/server/web-sessions'));
  await trialPage.mouse.click(180, 574);
  assert.equal((await trialLogin).status(), 201);
  await trialPage.setViewportSize({ width: 1280, height: 800 });
  await trialPage.locator('flt-semantics-placeholder').evaluate(element => element.click());
  await trialPage.keyboard.press('Meta+3');
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Perf fixture, active' }).first().click();
  const trialOpen = trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Try configuration' }).first();
  const draftRead = trialPage.waitForResponse(response =>
    response.url().endsWith('/api/v1/environments/fixture.perf/draft'));
  await trialOpen.click();
  assert.equal((await draftRead).status(), 200);
  await trialPage.evaluate(() => new Promise(requestAnimationFrame));
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Run preview' }).first().waitFor();
  const dryRunResponse = trialPage.waitForResponse(response =>
    response.url().endsWith('/api/v1/environments/fixture.perf/actions/dry-run'));
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Run preview' }).first().click();
  const dryRunHTTP = await dryRunResponse;
  assert.equal(dryRunHTTP.status(), 200);
  const dryRun = await dryRunHTTP.json();
  assert.equal(dryRun.source, 'published');
  assert.equal(dryRun.result.routeId, routeId);
  assert.equal(dryRun.result.accountId, account.id);
  assert.equal(dryRun.result.providerOrigin, endpoint.origin);
  await trialPage.locator('flt-semantics')
    .filter({ hasText: 'Predicted decision' }).first().waitFor();
  await trialPage.locator('flt-semantics')
    .filter({ hasText: 'Not live' }).first().waitFor();
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Published r1' }).last().click();
  await trialPage.locator('flt-semantics[role="menuitem"][aria-label*="Draft r' +
    unpublished.draftRevision + '"]').last().click({ timeout: 10_000 });
  const draftDryRunResponse = trialPage.waitForResponse(response =>
    response.url().endsWith('/api/v1/environments/fixture.perf/actions/dry-run'));
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Run preview' }).first().click();
  const draftHTTP = await draftDryRunResponse;
  assert.equal(draftHTTP.status(), 200);
  const draftDecision = await draftHTTP.json();
  assert.equal(draftDecision.source, 'draft');
  assert.equal(draftDecision.draftRevision, unpublished.draftRevision);
  assert.equal(draftDecision.publishedRevision, 1);
  assert.equal(draftDecision.result.effectiveModel, 'synthetic-draft-model');
  assert.equal(draftDecision.result.modelMapped, true);
  assert.ok(draftDecision.result.protocolChangedTopLevelFields.includes('model'));
  await trialPage.locator('flt-semantics')
    .filter({ hasText: 'Protocol and model changes' }).first().waitFor();
  await trialPage.locator('flt-semantics')
    .filter({ hasText: 'This draft is not active' }).first().waitFor();
  await trialPage.locator('flt-semantics')
    .filter({ hasText: 'Predicted decision' }).first().scrollIntoViewIfNeeded();
  await trialPage.setViewportSize({ width: 390, height: 760 });
  const narrowRun = await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Run preview' }).first().boundingBox();
  assert.ok(narrowRun && narrowRun.x >= 0 && narrowRun.x + narrowRun.width <= 390 &&
    narrowRun.y >= 0 && narrowRun.y + narrowRun.height <= 760,
  '390px preview action is outside the viewport');
  await trialPage.setViewportSize({ width: 1280, height: 800 });
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Cancel' }).last().click();
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'System Transparent, active' }).last().click({ timeout: 10_000 });
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Try configuration' }).first().click();
  const originalDryRunResponse = trialPage.waitForResponse(response =>
    response.url().endsWith('/api/v1/environments/system_transparent/actions/dry-run'));
  await trialPage.locator('flt-semantics[role="button"]')
    .filter({ hasText: 'Run preview' }).first().click();
  const originalHTTP = await originalDryRunResponse;
  assert.equal(originalHTTP.status(), 200);
  const originalDecision = await originalHTTP.json();
  assert.equal(originalDecision.result.destinationKind, 'original');
  assert.equal(originalDecision.result.routeId, '');
  assert.equal(originalDecision.result.accountId, '');
  await trialPage.close();

  async function measure(latencyMs, label) {
    const review = await json(await fetch(
      origin + '/api/v1/manual-captures/context?environmentId=' + environmentId,
      { headers: { Origin: origin, Authorization: 'Bearer ' + owner.readToken } },
    ), 200, 'manual context');
    const grant = await json(await fetch(origin + '/api/v1/manual-captures', {
      method: 'POST', headers: { Origin: origin,
        Authorization: 'Bearer ' + owner.writeToken,
        'Content-Type': 'application/json' },
      body: JSON.stringify({ environmentId, displayName: 'Perf ' + label,
        clientClass: 'desktop_app', lifetime: 'until_revoked',
        confirmationToken: review.confirmationToken }),
    }), 201, 'manual capture');
    const page = await browser.newPage({ viewport: { width: 390, height: 760 } });
    if (latencyMs > 0) {
      await page.route('**/api/v1/**', async route => {
        await new Promise(resolve => setTimeout(resolve, latencyMs));
        await route.continue();
      });
    }
    const apiResponses = [];
    page.on('response', response => {
      const url = new URL(response.url());
      if (url.searchParams.get('manualCaptureId') !== grant.capture.id ||
          !['/api/v1/activities', '/api/v1/conversations'].includes(url.pathname)) return;
      void response.finished().then(() => apiResponses.push({
        path: url.pathname, conversationId: url.searchParams.get('conversationId'),
        finished: performance.now(), status: response.status(),
      })).catch(() => {});
    });
    await page.goto(origin);
    await page.locator('input#username').waitFor();
    await page.locator('input#username').fill('playwright-owner');
    // Flutter makes its password input visible only after the field is tapped.
    await page.mouse.click(150, 527);
    await page.keyboard.type(password);
    const login = page.waitForResponse(response =>
      response.url().endsWith('/api/v1/server/web-sessions'));
    await page.mouse.click(180, 574);
    assert.equal((await login).status(), 201);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.locator('flt-semantics-placeholder').evaluate(element => element.click());
    await page.locator('flt-semantics[role="button"]')
      .filter({ hasText: 'Perf ' + label }).first().waitFor();

    const values = [];
    const shortSamples = 24;
    for (let index = 0; index < shortSamples + 2; index++) {
      if (index > 0) await new Promise(resolve =>
        setTimeout(resolve, (index * 371) % 1000));
      const marker = 'SYNTHETIC_PERF_' + label + '_' + String(index).padStart(3, '0');
      const prompt = index === shortSamples ? marker + 'x'.repeat(9_472)
        : index === shortSamples + 1
          ? marker + 'x'.repeat(151_552) + '_TAIL_SENTINEL' : marker;
      await send(grant, rootCA.certificatePem, prompt);
      const completed = performance.now();
      try {
        await page.locator('flt-semantics[role="button"][aria-label*="' + marker + '"]')
          .first().waitFor({ timeout: 5_000 });
      } catch (error) {
        const activity = await json(await fetch(origin + '/api/v1/activities?manualCaptureId=' + grant.capture.id + '&limit=30', {
          headers: { Origin: origin, Authorization: 'Bearer ' + owner.readToken },
        }), 200, 'activity after timeout');
        console.log('TIMEOUT', label, index, 'apiCount', activity.items.length,
          'ariaCount', await page.locator('flt-semantics[aria-label*="' + marker + '"]').count(),
          'recentResponses', apiResponses.slice(-15));
        throw error;
      }
      await page.evaluate(() => new Promise(requestAnimationFrame));
      const visible = performance.now();
      const responses = apiResponses.filter(item =>
        item.finished >= completed && item.finished <= visible);
      const probe = responses.find(item =>
        item.path === '/api/v1/activities' && item.conversationId == null);
      const directory = responses.find(item => item.path === '/api/v1/conversations');
      const selected = responses.findLast(item =>
        item.path === '/api/v1/activities' && item.conversationId != null);
      values.push({ bytes: prompt.length, endToVisibleMs: visible - completed,
        probeFinishMs: probe == null ? null : probe.finished - completed,
        directoryFinishMs: directory == null ? null : directory.finished - completed,
        selectedApiFinishMs: selected == null ? null : selected.finished - completed,
        selectedApiToVisibleMs: selected == null ? null : visible - selected.finished });
    }
    async function expandCurrent() {
      const expand = page.locator('flt-semantics[role="button"]')
        .filter({ hasText: 'Show all content' }).last();
      await expand.waitFor();
      const started = performance.now();
      await expand.click();
      await page.evaluate(() => new Promise(requestAnimationFrame));
      const elapsed = performance.now() - started;
      await page.mouse.move(800, 600);
      await page.mouse.wheel(0, 200_000);
      await page.locator('flt-semantics[role="button"]')
        .filter({ hasText: 'Show first 15 lines' }).last()
        .waitFor({ timeout: 30_000 });
      return elapsed;
    }
    const expandToVisibleMs = await expandCurrent();
    const paragraphMarker = 'SYNTHETIC_PERF_' + label + '_PARAGRAPHS';
    const paragraphPrompt = paragraphMarker + '\n\n' +
      Array(4096).fill('Synthetic **paragraph** and `code`.\n\n').join('') +
      'TAIL_SENTINEL';
    await send(grant, rootCA.certificatePem, paragraphPrompt);
    const paragraphCompleted = performance.now();
    await page.locator('flt-semantics[role="button"][aria-label*="' + paragraphMarker + '"]')
      .first().waitFor({ timeout: 5_000 });
    await page.evaluate(() => new Promise(requestAnimationFrame));
    const paragraphEndToVisibleMs = performance.now() - paragraphCompleted;
    const paragraphExpandToVisibleMs = await expandCurrent();
    let failureDiagnosis = null;
    if (label === 'delayed') {
      const stoppedProvider = provider;
      provider = null;
      stoppedProvider.closeAllConnections();
      await new Promise(resolve => stoppedProvider.close(resolve));
      const failureMarker = 'SYNTHETIC_PERF_CONNECTION_FAILURE';
      await send(grant, rootCA.certificatePem, failureMarker, false);
      let failed;
      for (let index = 0; index < 50 && failed == null; index++) {
        const activities = await json(await fetch(origin + '/api/v1/activities?manualCaptureId=' +
          grant.capture.id + '&limit=10', {
          headers: { Origin: origin, Authorization: 'Bearer ' + owner.readToken },
        }), 200, 'failure activity');
        failed = activities.items.find(item =>
          item.requestPreview?.text?.includes(failureMarker) && item.status === 'failed');
        if (failed == null) await new Promise(resolve => setTimeout(resolve, 100));
      }
      assert.ok(failed, 'connection failure was not recorded');
      assert.equal(failed.reasonCode, 'provider_transport_failed');
      const detail = await json(await fetch(origin + '/api/v1/exchanges/' +
        encodeURIComponent(failed.id) + '?contentView=incremental', {
        headers: { Origin: origin, Authorization: 'Bearer ' + owner.readToken },
      }), 200, 'failure detail');
      const attempt = detail.processingTrace.attempts.findLast(item =>
        item.purpose === 'provider_attempt');
      assert.equal(attempt?.errorClass, 'connection_failed');
      const directory = await json(await fetch(origin + '/api/v1/conversations?manualCaptureId=' +
        grant.capture.id + '&limit=200', {
        headers: { Origin: origin, Authorization: 'Bearer ' + owner.readToken },
      }), 200, 'failure directory');
      assert.equal(directory.items[0]?.conversation?.id, 'exchange:' + failed.id);
      const failureCard = page.locator('flt-semantics[role="button"][aria-label*="' +
        failureMarker + '"]').last();
      await failureCard.waitFor({ timeout: 10_000 });
      await failureCard.click();
      const failureNotice = page.locator(
        'flt-semantics[aria-label*="The outbound connection failed or was interrupted."]',
      ).last();
      await failureNotice.waitFor({ timeout: 10_000 });
      assert.ok(!(await failureNotice.getAttribute('aria-label'))
        .includes('Waiting for the terminal response'));
      failureDiagnosis = { reasonCode: failed.reasonCode, errorClass: attempt.errorClass,
        browserMessageVisible: true };
    }
    await page.close();
    const warm = values.slice(1, shortSamples);
    return { label, latencyMs, samples: values.length,
      first: values[0], endToVisible: stats(warm, 'endToVisibleMs'),
      probeFinish: stats(warm, 'probeFinishMs'),
      directoryFinish: stats(warm, 'directoryFinishMs'),
      selectedApiFinish: stats(warm, 'selectedApiFinishMs'),
      selectedApiToVisible: stats(warm, 'selectedApiToVisibleMs'),
      long9k: values[shortSamples], long151k: values[shortSamples + 1],
      expandToVisibleMs, paragraphBytes: paragraphPrompt.length,
      paragraphEndToVisibleMs, paragraphExpandToVisibleMs, failureDiagnosis };
  }

  const local = await measure(0, 'local');
  const delayed = await measure(80, 'delayed');
  console.log(JSON.stringify({
    dryRun: { publishedRoute: dryRun.result.routeId,
      draftModel: draftDecision.result.effectiveModel,
      originalDestination: originalDecision.result.destinationKind,
      narrowActionVisible: true },
    local, delayed, browser: await browser.version(),
    runtime: process.platform + '/' + process.arch }));
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode == null) {
    child.kill('SIGTERM');
    await new Promise(resolve => child.once('exit', resolve));
  }
  if (provider) await new Promise(resolve => provider.close(resolve));
  await rm(fixture, { recursive: true });
}
