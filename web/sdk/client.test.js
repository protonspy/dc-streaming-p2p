import test from "node:test";
import assert from "node:assert/strict";

import { AuthError, CallState, KeyMismatchError, Path, PeerClient, SUBPROTOCOL, TOKEN_PREFIX } from "./client.js";
import { FakePeerConnection, FakeServer, FakeSocket, fakeStream, statsFor } from "./fakes.js";

/** A client wired to the fakes, started unless the test asks otherwise. */
async function startedClient(overrides = {}) {
  FakeSocket.reset();
  FakePeerConnection.reset();

  const server = new FakeServer();
  const client = new PeerClient({
    serverURL: "https://central.example",
    clientID: "sdk-web",
    clientSecret: "a-secret-long-enough-for-the-server",
    fetch: server.fetch,
    WebSocket: FakeSocket,
    RTCPeerConnection: FakePeerConnection,
    sleep: async () => {},
    random: () => 0.5,
    ...overrides,
  });

  await client.start();
  return { client, server };
}

/** Runs the microtask queue so the SDK's own promises settle. */
async function settle() {
  for (let i = 0; i < 10; i += 1) await Promise.resolve();
}

test("start authenticates, registers, reads the ICE configuration and opens the channel", async () => {
  const { client, server } = await startedClient();

  assert.equal(client.peerId, "peer-001");
  assert.deepEqual(
    server.requests.map((request) => `${request.method} ${request.path}`),
    ["POST /auth", "POST /peers", "GET /ice-config"],
  );

  const socket = FakeSocket.last;
  assert.equal(socket.url, "wss://central.example/signal");
  assert.deepEqual(socket.protocols, [SUBPROTOCOL, TOKEN_PREFIX + "token-1"]);
});

test("the credential is never sent to a server publishing another key", async () => {
  FakeSocket.reset();
  const server = new FakeServer({ publicKey: "some-other-key" });
  const client = new PeerClient({
    serverURL: "https://central.example",
    clientID: "sdk-web",
    clientSecret: "a-secret-long-enough-for-the-server",
    publicKey: "the-key-we-expected",
    fetch: server.fetch,
    WebSocket: FakeSocket,
    RTCPeerConnection: FakePeerConnection,
  });

  await assert.rejects(() => client.start(), KeyMismatchError);
  assert.equal(server.sentTo("/auth").length, 0, "a credential was sent to a server with the wrong key");
  assert.equal(server.authCalls, 0);
});

test("a matching key is accepted and the credential follows", async () => {
  FakeSocket.reset();
  const server = new FakeServer({ publicKey: "the-published-key" });
  const client = new PeerClient({
    serverURL: "https://central.example",
    clientID: "sdk-web",
    clientSecret: "a-secret-long-enough-for-the-server",
    publicKey: "the-published-key",
    fetch: server.fetch,
    WebSocket: FakeSocket,
    RTCPeerConnection: FakePeerConnection,
  });

  await client.start();
  assert.equal(server.sentTo("/auth").length, 1);
});

test("a refused credential is reported and not retried", async () => {
  FakeSocket.reset();
  const server = new FakeServer();
  server.authStatus = 401;

  const client = new PeerClient({
    serverURL: "https://central.example",
    clientID: "sdk-web",
    clientSecret: "wrong",
    fetch: server.fetch,
    WebSocket: FakeSocket,
    RTCPeerConnection: FakePeerConnection,
  });

  await assert.rejects(() => client.start(), AuthError);
  assert.equal(server.authCalls, 1, "the same credential was presented more than once");
});

test("calling opens a session and offers", async () => {
  const { client, server } = await startedClient();

  const call = await client.call("peer-002", { localStream: fakeStream() });

  assert.equal(call.sessionId, "sess-1");
  assert.equal(call.state, CallState.Connecting);
  assert.equal(server.sentTo("/sessions", "POST").length, 1);

  const offers = FakeSocket.last.sentOf("offer");
  assert.equal(offers.length, 1);
  assert.equal(offers[0].session_id, "sess-1");
  assert.equal(offers[0].payload.type, "offer");

  const connection = FakePeerConnection.last;
  assert.equal(connection.localDescription.type, "offer");
  assert.equal(connection.addedTracks.length, 2, "the local audio and video were not sent");
});

test("candidates are carried to the other side", async () => {
  const { client } = await startedClient();

  await client.call("peer-002");
  FakePeerConnection.last.fireCandidate({ candidate: "candidate:1 1 udp …" });

  const sent = FakeSocket.last.sentOf("candidate");
  assert.equal(sent.length, 1);
  assert.equal(sent[0].session_id, "sess-1");
});

test("an offer for a session this side did not start is answered", async () => {
  const { client } = await startedClient();

  const calls = [];
  client.on("call", (call) => calls.push(call));

  FakeSocket.last.deliver({
    type: "offer",
    session_id: "sess-9",
    from: "peer-002",
    payload: { type: "offer", sdp: "v=0 from the caller" },
  });
  await settle();

  assert.equal(calls.length, 1, "the application was not told about the incoming call");
  assert.equal(calls[0].peerId, "peer-002");
  assert.equal(calls[0].isCaller, false);

  const answers = FakeSocket.last.sentOf("answer");
  assert.equal(answers.length, 1);
  assert.equal(answers[0].session_id, "sess-9");
  assert.equal(FakePeerConnection.last.remoteDescription.sdp, "v=0 from the caller");
});

test("an answer and a candidate for a held session reach the connection", async () => {
  const { client } = await startedClient();

  await client.call("peer-002");
  const connection = FakePeerConnection.last;

  FakeSocket.last.deliver({
    type: "answer",
    session_id: "sess-1",
    from: "peer-002",
    payload: { type: "answer", sdp: "v=0 answer" },
  });
  FakeSocket.last.deliver({
    type: "candidate",
    session_id: "sess-1",
    from: "peer-002",
    payload: { candidate: "candidate:2 1 udp …" },
  });
  await settle();

  assert.equal(connection.remoteDescription.sdp, "v=0 answer");
  assert.equal(connection.addedCandidates.length, 1);
});

test("a message for a session this side does not have is discarded", async () => {
  const { client } = await startedClient();

  const errors = [];
  client.on("error", (err) => errors.push(err));

  FakeSocket.last.deliver({ type: "answer", session_id: "sess-nobody-has", payload: {} });
  FakeSocket.last.deliver({ type: "candidate", session_id: "sess-nobody-has", payload: {} });
  FakeSocket.last.deliver({ type: "bye", session_id: "sess-nobody-has" });
  await settle();

  assert.deepEqual(errors, [], "a message for an unknown session was treated as a failure");
  assert.equal(client.calls.length, 0);
});

test("a type this version does not know is ignored", async () => {
  const { client } = await startedClient();

  const errors = [];
  client.on("error", (err) => errors.push(err));

  FakeSocket.last.deliver({ type: "something-newer", session_id: "sess-1" });
  await settle();

  assert.deepEqual(errors, []);
});

for (const [candidateType, expected] of [
  ["relay", Path.Relayed],
  ["srflx", Path.Direct],
  ["host", Path.Direct],
]) {
  test(`a connection over a ${candidateType} candidate reports ${expected || "an unknown path"}`, async () => {
    const { client, server } = await startedClient();

    const call = await client.call("peer-002");
    const states = [];
    call.on("state", (change) => states.push(change));

    const connection = FakePeerConnection.last;
    connection.stats = statsFor(candidateType);
    connection.moveTo("connected");
    await settle();

    assert.equal(call.state, CallState.Connected);
    assert.equal(call.path, expected);
    assert.deepEqual(states, [{ state: CallState.Connected, path: expected }]);

    const reported = server.reportedStates();
    assert.deepEqual(reported, [{ state: "connected", path: expected }]);
  });
}

test("the far side's media is handed to the application", async () => {
  const { client } = await startedClient();

  const call = await client.call("peer-002");
  const received = [];
  call.on("track", (stream) => received.push(stream));

  const stream = fakeStream();
  FakePeerConnection.last.fireTrack(stream);

  assert.deepEqual(received, [stream]);
  assert.equal(call.remoteStream, stream);
});

test("a connection that fails is reported to the application and the control plane", async () => {
  const { client, server } = await startedClient();

  const call = await client.call("peer-002");
  const states = [];
  call.on("state", (change) => states.push(change));

  FakePeerConnection.last.moveTo("failed");
  await settle();

  assert.equal(call.state, CallState.Failed);
  assert.deepEqual(states.at(-1), { state: CallState.Failed, path: Path.Unknown });
  assert.deepEqual(server.reportedStates(), [{ state: "failed" }]);
  assert.equal(client.calls.length, 0, "a failed call was left open");
});

test("closing a call closes the connection, the session and the local media", async () => {
  const { client, server } = await startedClient();

  const localStream = fakeStream();
  const call = await client.call("peer-002", { localStream });
  const connection = FakePeerConnection.last;

  await call.close();

  assert.equal(connection.closed, true);
  assert.equal(call.state, CallState.Closed);
  assert.deepEqual(server.reportedStates(), [{ state: "closed" }]);
  assert.ok(localStream.tracks.every((track) => track.stopped), "the camera was left running");
  assert.equal(FakeSocket.last.sentOf("bye").length, 1, "the far side was not told");
  assert.equal(client.calls.length, 0);
});

test("the far side ending a call closes it here without telling anybody twice", async () => {
  const { client, server } = await startedClient();

  const call = await client.call("peer-002");
  const before = server.reportedStates().length;

  FakeSocket.last.deliver({ type: "bye", session_id: "sess-1", from: "peer-002" });
  await settle();

  assert.equal(call.state, CallState.Closed);
  assert.equal(client.calls.length, 0);
  assert.equal(server.reportedStates().length, before, "the server was told about a call it ended");
  assert.equal(FakeSocket.last.sentOf("bye").length, 0, "a bye was echoed back at the peer that sent one");
});

test("a session reported closed by the server ends the call", async () => {
  const { client } = await startedClient();

  const call = await client.call("peer-002");

  FakeSocket.last.deliver({ type: "session", session_id: "sess-1", state: "closed" });
  await settle();

  assert.equal(call.state, CallState.Closed);
});

test("a channel that drops is reopened, and a call still negotiating fails", async () => {
  const { client } = await startedClient();

  const call = await client.call("peer-002");
  const first = FakeSocket.last;

  first.close();
  await settle();

  assert.equal(call.state, CallState.Failed, "a call that cannot finish negotiating was left hanging");
  assert.ok(FakeSocket.opened.length >= 2, "the channel was not reopened");
  assert.equal(FakeSocket.last.closed, false);
});

test("a connected call survives the channel dropping", async () => {
  const { client } = await startedClient();

  const call = await client.call("peer-002");
  const connection = FakePeerConnection.last;
  connection.stats = statsFor("host");
  connection.moveTo("connected");
  await settle();

  FakeSocket.last.close();
  await settle();

  assert.equal(call.state, CallState.Connected, "an established peer connection does not need the server");
});

test("the reconnect delay grows and carries jitter", async () => {
  const { client } = await startedClient({ random: () => 1 });

  const delays = [0, 1, 2, 3, 10].map((attempt) => client._backoffDelay(attempt));

  assert.ok(delays[1] > delays[0], "the delay does not grow");
  assert.ok(delays[2] > delays[1]);
  assert.equal(delays[4], 30_000, "the delay is not capped");

  const jittered = new PeerClient({
    serverURL: "https://central.example",
    clientID: "sdk-web",
    clientSecret: "a-secret-long-enough-for-the-server",
    random: () => 0,
  });
  assert.ok(
    jittered._backoffDelay(3) < client._backoffDelay(3),
    "two peers reconnecting take the same delay, which turns one restart into a second outage",
  );
});

test("a token near expiry is replaced without the application acting", async () => {
  FakeSocket.reset();
  const server = new FakeServer();
  server.tokenTTL = 20_000; // inside the refresh margin from the moment it is issued

  const client = new PeerClient({
    serverURL: "https://central.example",
    clientID: "sdk-web",
    clientSecret: "a-secret-long-enough-for-the-server",
    fetch: server.fetch,
    WebSocket: FakeSocket,
    RTCPeerConnection: FakePeerConnection,
  });

  await client.start();
  const authsAfterStart = server.authCalls;

  await client.call("peer-002");

  assert.ok(server.authCalls > authsAfterStart, "an expiring token was carried into the next request");
});

test("closing the client closes its calls, its channel and its registration", async () => {
  const { client, server } = await startedClient();

  const localStream = fakeStream();
  await client.call("peer-002", { localStream });

  await client.close();

  assert.equal(client.calls.length, 0);
  assert.equal(FakeSocket.last.closed, true);
  assert.equal(server.sentTo("/peers", "DELETE").length, 1);
  assert.ok(localStream.tracks.every((track) => track.stopped));
});

test("a closed client does not reconnect", async () => {
  const { client } = await startedClient();

  await client.close();
  const opened = FakeSocket.opened.length;
  await settle();

  assert.equal(FakeSocket.opened.length, opened, "a client the application closed reopened its channel");
});

test("nothing carrying a session description reaches the application", async () => {
  const { client } = await startedClient();

  const seen = [];
  const call = await client.call("peer-002");
  call.on("state", (change) => seen.push(change));

  const connection = FakePeerConnection.last;
  connection.stats = statsFor("host");
  connection.moveTo("connected");
  await settle();

  for (const change of seen) {
    assert.deepEqual(
      Object.keys(change).sort(),
      ["path", "state"],
      "the application was handed more than the state and the path",
    );
  }
});

test("a listener that throws does not stop the others", async () => {
  const { client } = await startedClient();

  const call = await client.call("peer-002");
  const reached = [];
  call.on("state", () => {
    throw new Error("an application bug");
  });
  call.on("state", (change) => reached.push(change));

  await call.close();

  assert.equal(reached.length, 1, "one listener throwing stopped the next");
});

test("a negotiation that throws leaves no call behind", async () => {
  const { client, server } = await startedClient();

  // Restored rather than deleted: deleting takes the class's own method with it.
  const realCreateOffer = FakePeerConnection.prototype.createOffer;
  FakePeerConnection.prototype.createOffer = async function () {
    throw new Error("negotiation blew up");
  };

  await assert.rejects(() => client.call("peer-002"), /negotiation blew up/);
  await settle();

  assert.equal(client.calls.length, 0, "a call nobody can reach was left open");
  assert.equal(FakePeerConnection.last.closed, true, "its connection was left open");
  assert.deepEqual(server.reportedStates(), [{ state: "failed" }], "the control plane was not told");

  FakePeerConnection.prototype.createOffer = realCreateOffer;
});

test("an offer that cannot be answered leaves no call behind", async () => {
  const { client, server } = await startedClient();

  const realSetRemote = FakePeerConnection.prototype.setRemoteDescription;
  FakePeerConnection.prototype.setRemoteDescription = async function () {
    throw new Error("bad offer");
  };

  const errors = [];
  client.on("error", (err) => errors.push(err));

  FakeSocket.last.deliver({
    type: "offer",
    session_id: "sess-9",
    from: "peer-002",
    payload: { type: "offer", sdp: "v=0 nonsense" },
  });
  await settle();

  assert.equal(errors.length, 1);
  assert.equal(client.calls.length, 0, "a call that could not be answered was left open");
  assert.deepEqual(server.reportedStates(), [{ state: "failed" }]);

  FakePeerConnection.prototype.setRemoteDescription = realSetRemote;
});

test("closing while a reconnect is in flight leaves nothing open", async () => {
  const { client, server } = await startedClient({ sleep: async () => {} });

  // Drop the channel, then close the client while it is reopening.
  FakeSocket.last.close();
  await client.close();
  await settle();

  assert.equal(client.calls.length, 0);
  assert.equal(FakeSocket.last.closed, true, "a closed client was left holding a socket");

  const registrations = server.sentTo("/peers", "POST").length;
  const deregistrations = server.sentTo("/peers", "DELETE").length;
  assert.ok(
    deregistrations >= 1,
    "a closed client kept its registration",
  );
  assert.ok(
    registrations <= deregistrations + 1,
    `registered ${registrations} times and deregistered ${deregistrations}; a closed client re-registered`,
  );
});

test("a credential the server refuses stops the reconnect loop", async () => {
  const { client, server } = await startedClient();

  server.authStatus = 401;
  // The token it holds is spent, so reconnecting has to authenticate again.
  client._tokenExpiresAt = Date.now();

  const errors = [];
  client.on("error", (err) => errors.push(err));

  FakeSocket.last.close();
  await settle();

  assert.ok(errors.some((err) => err instanceof AuthError), "the refusal was not reported");
  const attempts = server.authCalls;
  await settle();
  assert.equal(server.authCalls, attempts, "a refused credential is being retried forever");
});

test("a token can come from the application rather than a secret in the page", async () => {
  FakeSocket.reset();
  FakePeerConnection.reset();

  const server = new FakeServer();
  let asked = 0;

  const client = new PeerClient({
    serverURL: "https://central.example",
    clientID: null,
    clientSecret: null,
    getToken: async () => {
      asked += 1;
      return {
        token: "from-the-application",
        peer_id: "peer-001",
        expires_at: new Date(Date.now() + 3600_000).toISOString(),
      };
    },
    fetch: server.fetch,
    WebSocket: FakeSocket,
    RTCPeerConnection: FakePeerConnection,
  });

  await client.start();

  assert.equal(asked, 1);
  assert.equal(server.sentTo("/auth").length, 0, "a secret was sent despite the application supplying the token");
  assert.deepEqual(FakeSocket.last.protocols, [SUBPROTOCOL, TOKEN_PREFIX + "from-the-application"]);
});

test("a client with neither a secret nor a token source is refused", () => {
  assert.throws(
    () => new PeerClient({ serverURL: "https://central.example" }),
    /no credentials/,
  );
});

test("a retried request keeps the body it was refused with", async () => {
  const { client, server } = await startedClient();

  const call = await client.call("peer-002");

  // The next request is refused once, so the SDK re-authenticates and repeats it.
  let refusals = 0;
  const underlying = server.fetch;
  const refuseOnce = async (url, init) => {
    if (new URL(url).pathname.endsWith("/state") && refusals === 0) {
      refusals += 1;
      return { status: 401, ok: false, json: async () => ({ error: "token_expired" }) };
    }
    return underlying(url, init);
  };
  client._fetch = refuseOnce;

  await call.close();

  const reported = server.reportedStates();
  assert.deepEqual(reported, [{ state: "closed" }], "the retry dropped what it was reporting");
});
