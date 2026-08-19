// Fakes standing in for what a browser provides, so the SDK's own logic can be
// tested without a browser or a network. They record what they were told and fire
// what a test asks them to fire; they do not pretend to implement WebRTC.

/** A control plane that answers the routes the SDK calls. */
export class FakeServer {
  constructor({ peerId = "peer-001", publicKey = "the-published-key" } = {}) {
    this.peerId = peerId;
    this.publicKey = publicKey;
    this.requests = [];
    this.authCalls = 0;
    this.tokenTTL = 3600_000;
    this.authStatus = 200;
    this.iceServers = [{ urls: ["stun:stun.example:3478"] }];
    this.relayAvailable = false;
    this.sessionId = "sess-1";
    this.now = () => Date.now();
  }

  /** The fetch the SDK is given. */
  fetch = async (url, init = {}) => {
    const { pathname } = new URL(url);
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(init.body) : null;
    this.requests.push({ method, path: pathname, body, headers: init.headers ?? {} });

    if (pathname === "/.well-known/jwks.json") {
      return json(200, {
        keys: [{ kty: "OKP", crv: "Ed25519", use: "sig", alg: "EdDSA", x: this.publicKey }],
      });
    }

    if (pathname === "/auth") {
      this.authCalls += 1;
      if (this.authStatus !== 200) return json(this.authStatus, { error: "invalid_client" });
      return json(200, {
        token: `token-${this.authCalls}`,
        peer_id: this.peerId,
        expires_at: new Date(this.now() + this.tokenTTL).toISOString(),
      });
    }

    if (pathname === "/peers" && method === "POST") {
      return json(200, { peer_id: this.peerId, state: "online", heartbeat_interval: "15s" });
    }
    if (pathname === "/peers" && method === "DELETE") {
      return new FakeResponse(204, null);
    }

    if (pathname === "/ice-config") {
      return json(200, {
        ice_servers: this.iceServers,
        relay_available: this.relayAvailable,
      });
    }

    if (pathname === "/sessions" && method === "POST") {
      return json(201, {
        session_id: this.sessionId,
        caller: this.peerId,
        callee: body.peer_id,
        state: "negotiating",
      });
    }

    if (pathname.startsWith("/sessions/")) {
      return json(200, { session_id: this.sessionId, state: body?.state ?? "closed" });
    }

    return json(404, { error: "not_found" });
  };

  /** What was sent to one route, in order. */
  sentTo(path, method = null) {
    return this.requests.filter(
      (request) => request.path === path && (!method || request.method === method),
    );
  }

  /** Every state a session was reported as. */
  reportedStates() {
    return this.requests
      .filter((request) => request.path.endsWith("/state"))
      .map((request) => request.body);
  }
}

class FakeResponse {
  constructor(status, body) {
    this.status = status;
    this.ok = status >= 200 && status < 300;
    this._body = body;
  }

  async json() {
    return this._body;
  }
}

function json(status, body) {
  return new FakeResponse(status, body);
}

/**
 * A WebSocket that a test drives: what the SDK sent is recorded, and what the
 * server would send is delivered by hand.
 */
export class FakeSocket {
  static opened = [];

  constructor(url, protocols) {
    this.url = url;
    this.protocols = protocols;
    this.readyState = 1;
    this.sent = [];
    this.closed = false;
    FakeSocket.opened.push(this);
  }

  send(raw) {
    this.sent.push(JSON.parse(raw));
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.readyState = 3;
    this.onclose?.({});
  }

  /** Delivers a message as the server would. */
  deliver(message) {
    this.onmessage?.({ data: JSON.stringify(message) });
  }

  /** What the SDK sent, of one type. */
  sentOf(type) {
    return this.sent.filter((message) => message.type === type);
  }

  static reset() {
    FakeSocket.opened = [];
  }

  static get last() {
    return FakeSocket.opened.at(-1);
  }
}

/**
 * A peer connection that records what it was told and fires what a test wants. It
 * is not an implementation of WebRTC: nothing here parses a session description,
 * because what WebRTC does with one is not this codebase's to verify.
 */
export class FakePeerConnection {
  static created = [];

  constructor(config) {
    this.config = config;
    this.connectionState = "new";
    this.localDescription = null;
    this.remoteDescription = null;
    this.addedCandidates = [];
    this.addedTracks = [];
    this.closed = false;
    this.stats = null;
    FakePeerConnection.created.push(this);
  }

  async createOffer() {
    return { type: "offer", sdp: "v=0 offer" };
  }

  async createAnswer() {
    return { type: "answer", sdp: "v=0 answer" };
  }

  async setLocalDescription(description) {
    this.localDescription = description;
  }

  async setRemoteDescription(description) {
    this.remoteDescription = description;
  }

  async addIceCandidate(candidate) {
    this.addedCandidates.push(candidate);
  }

  addTrack(track, stream) {
    this.addedTracks.push({ track, stream });
  }

  close() {
    this.closed = true;
  }

  async getStats() {
    return this.stats ?? new Map();
  }

  /** Fires a candidate as the browser would. */
  fireCandidate(candidate) {
    this.onicecandidate?.({ candidate });
  }

  /** Fires the far side's media. */
  fireTrack(stream) {
    this.ontrack?.({ streams: [stream] });
  }

  /** Moves the connection and fires the change. */
  moveTo(state) {
    this.connectionState = state;
    this.onconnectionstatechange?.({});
  }

  static reset() {
    FakePeerConnection.created = [];
  }

  static get last() {
    return FakePeerConnection.created.at(-1);
  }
}

/** Stats describing a connection that ended up on the given path. */
export function statsFor(candidateType) {
  const reports = new Map();
  reports.set("transport-1", {
    id: "transport-1",
    type: "transport",
    selectedCandidatePairId: "pair-1",
  });
  reports.set("pair-1", {
    id: "pair-1",
    type: "candidate-pair",
    selected: true,
    localCandidateId: "local-1",
    remoteCandidateId: "remote-1",
  });
  reports.set("local-1", { id: "local-1", type: "local-candidate", candidateType });
  reports.set("remote-1", { id: "remote-1", type: "remote-candidate", candidateType: "host" });
  return reports;
}

/** A media stream whose tracks record being stopped. */
export function fakeStream() {
  const tracks = [
    { kind: "audio", stopped: false, stop() { this.stopped = true; } },
    { kind: "video", stopped: false, stop() { this.stopped = true; } },
  ];
  return { getTracks: () => tracks, tracks };
}
