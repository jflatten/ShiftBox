import { readProblem } from './api-problem';

function response(status: number, body: unknown, contentType = 'application/problem+json') {
  return { status, headers: new Headers({ 'Content-Type': contentType }), json: async () => body } as Response;
}

// A structured controller error retains actionable guidance and its diagnostic request reference.
test('reads a valid controller problem', async () => {
  const problem = await readProblem(response(409, { type: 'about:blank', status: 409, code: 'workspace.conflict', title: 'Refresh this workspace', detail: 'Another operation is running.', requestId: 'request-one', retryable: false, fields: { name: 'Choose another name.' } }));
  expect(problem.problem.title).toBe('Refresh this workspace');
  expect(problem.problem.requestId).toBe('request-one');
  expect(problem.problem.fields).toEqual({ name: 'Choose another name.' });
});

// Reverse-proxy HTML, invalid JSON and malformed problem bodies must never be rendered as diagnostic output.
test('replaces unexpected responses with safe recovery guidance', async () => {
  for (const bad of [response(502, 'private proxy traceback', 'text/html'), response(500, { title: 'private exception' }), response(403, { type: 'about:blank', status: 200, code: 'bad', title: 'private exception', requestId: '' }), { status: 500, headers: new Headers({ 'Content-Type': 'application/problem+json' }), json: async () => { throw new Error('private parser exception'); } } as unknown as Response]) {
    const problem = await readProblem(bad);
    expect(problem.problem.title).toBe('Unable to complete the request');
    expect(JSON.stringify(problem.problem)).not.toContain('private');
  }
});
