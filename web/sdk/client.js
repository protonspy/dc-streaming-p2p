// The browser SDK: one call that authenticates, registers, opens a session,
// negotiates, and hands back the far side's media.
//
// Everything it touches from outside — fetch, WebSocket, RTCPeerConnection — is
// injected and defaults to what a browser has, which is what lets its own logic be
// tested without a browser.

/** The subprotocol a signaling channel speaks. */
export const SUBPROTOCOL = "dc-signal.v1";

/** How the token rides alongside it: a browser cannot set headers on a WebSocket. */
export const TOKEN_PREFIX = "dc-token.";

/** What an application sees a call as. Nothing else is reported outward. */
export const CallState = Object.freeze({
  Connecting: "connecting",
  Connected: "connected",
  Failed: "failed",
  Closed: "closed",
});

/** How the media travelled, once it has. */
export const Path = Object.freeze({
  Direct: "direct",
  Relayed: "relayed",
  Unknown: "",
});

/** Thrown when the server publishes a key other than the one configured. */
export class KeyMismatchError extends Error {
  constructor() {
    super("the server published a different public key");
    this.name = "KeyMismatchError";
  }
}

/** Thrown when the control plane refuses a credential. */
export class AuthError extends Error {
  constructor(code) {
    super(`authentication refused: ${code}`);
    this.name = "AuthError";
    this.code = code;
  }
}

/**
 * A tiny event emitter. Small enough to own, and owning it is what keeps this
 * package dependency-free.
 */
class Emitter {
  #listeners = new Map();

  on(event, listener) {
    const held = this.#listeners.get(event) ?? [];
    held.push(listener);
    this.#listeners.set(event, held);
    return () => this.off(event, listener);
  }

  off(event, listener) {
    const held = this.#listeners.get(event);
    if (!held) return;
    this.#listeners.set(
      event,
      held.filter((held) => held !== listener),
    );
  }

  emit(event, payload) {
    for (const listener of this.#listeners.get(event) ?? []) {
      // One listener throwing must not stop the others, or an application bug
      // becomes an SDK failure. It is reported where the platform reports
      // uncaught errors rather than rethrown, which would take the process with
      // it in anything that is not a browser.
      try {
        listener(payload);
      } catch (err) {
        if (typeof globalThis.reportError === "function") {
          globalThis.reportError(err);
        } else {
          console.error("dc-sdk: a listener threw", err);
        }
      }
    }
  }
}

/**
 * One call: a peer connection, its state, and the media the far side sent.
 *
 * It exists from the moment a session does, including for a call this side did not
 * start — the offer arrives before the application has been told anything.
 */
export class Call extends Emitter {
  constructor({ sessionId, peerId, isCaller, connection, client }) {
    super();
    this.sessionId = sessionId;
    this.peerId = peerId;
    this.isCaller = isCaller;
    this.state = CallState.Connecting;
    this.path = Path.Unknown;
    this.remoteStream = null;

    this._connection = connection;
    this._client = client;
    this._localStream = null;
    this._closed = false;
  }

  /** Ends the call from this side. */
  async close() {
    await this._client._closeCall(this, CallState.Closed);
  }

  /** Moves the call and tells the application, once per change. */
  _moveTo(state, path = this.path) {
    if (this.state === state && this.path === path) return;
    this.state = state;
    this.path = path;
    this.emit("state", { state, path });
  }
}

/**
 * PeerClient is the whole SDK surface.
 */
export class PeerClient extends Emitter {
  /**
   * @param {object} options
   * @param {string} options.serverURL      where the control plane is
   * @param {string} options.clientID       the configured client
   * @param {string} options.clientSecret   its secret
   * @param {string} [options.publicKey]    the key it must publish, base64url
   * @param {typeof fetch} [options.fetch]
   * @param {typeof WebSocket} [options.WebSocket]
   * @param {typeof RTCPeerConnection} [options.RTCPeerConnection]
   * @param {(ms: number) => Promise<void>} [options.sleep]
   * @param {() => number} [options.random]  used only for reconnect jitter
   */
  constructor(options) {
    super();
    const {
      serverURL,
      clientID,
      clientSecret,
      publicKey = null,
      fetch: fetchImpl = globalThis.fetch?.bind(globalThis),
      WebSocket: WebSocketImpl = globalThis.WebSocket,
      RTCPeerConnection: RTCPeerConnectionImpl = globalThis.RTCPeerConnection,
      sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      random = Math.random,
    } = options ?? {};

    if (!serverURL) throw new Error("no server URL");
    if (!clientID || !clientSecret) throw new Error("no credentials");

    this.serverURL = serverURL.replace(/\/+$/, "");
    this.peerId = null;

    this._clientID = clientID;
    this._clientSecret = clientSecret;
    this._expectedKey = publicKey;
    this._fetch = fetchImpl;
    this._WebSocket = WebSocketImpl;
    this._RTCPeerConnection = RTCPeerConnectionImpl;
    this._sleep = sleep;
    this._random = random;

    this._token = null;
    this._tokenExpiresAt = 0;
    this._iceServers = [];
    this._socket = null;
    this._calls = new Map();
    this._closed = false;
    this._reconnectAttempt = 0;
  }

  /** Authenticates, registers, and opens the signaling channel. */
  async start() {
    this._closed = false;
    await this._authenticate();
    await this._register();
    await this._loadICE();
    await this._openChannel();
    return this.peerId;
  }

  /** Closes every call, the channel, and the registration. */
  async close() {
    this._closed = true;

    for (const call of [...this._calls.values()]) {
      await this._closeCall(call, CallState.Closed);
    }

    if (this._socket) {
      const socket = this._socket;
      this._socket = null;
      socket.close();
    }

    if (this._token) {
      // Best effort: the registry would drop it anyway, and a peer leaving
      // loudly is a far side that stops waiting sooner.
      await this._request("DELETE", "/peers").catch(() => {});
    }
  }

  /**
   * Opens a call to another peer.
   *
   * @param {string} peerId
   * @param {object} [options]
   * @param {MediaStream} [options.localStream] what to send
   */
  async call(peerId, { localStream = null } = {}) {
    const session = await this._request("POST", "/sessions", { peer_id: peerId });
    const call = this._callFor(session.session_id, peerId, true);
    call._localStream = localStream;

    this._addTracks(call, localStream);

    const offer = await call._connection.createOffer();
    await call._connection.setLocalDescription(offer);
    this._send({ type: "offer", session_id: call.sessionId, payload: offer });

    return call;
  }

  /** The calls this client currently holds. */
  get calls() {
    return [...this._calls.values()];
  }

  /** Sends what the application gave us, if it gave us anything. */
  _addTracks(call, stream) {
    for (const track of stream?.getTracks?.() ?? []) {
      call._connection.addTrack(track, stream);
    }
  }

  // ---------------------------------------------------------------- internals

  async _authenticate() {
    await this._verifyServerKey();

    const response = await this._fetch(`${this.serverURL}/auth`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        client_id: this._clientID,
        client_secret: this._clientSecret,
      }),
    });

    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      throw new AuthError(body.error ?? String(response.status));
    }

    const issued = await response.json();
    this._token = issued.token;
    this._tokenExpiresAt = Date.parse(issued.expires_at);
    this.peerId = issued.peer_id;
  }

  /**
   * Compares the published key with the configured one, before any credential is
   * sent. Without a configured key there is nothing to compare and nothing is
   * fetched.
   */
  async _verifyServerKey() {
    if (!this._expectedKey) return;

    const response = await this._fetch(`${this.serverURL}/.well-known/jwks.json`);
    if (!response.ok) throw new KeyMismatchError();

    const set = await response.json().catch(() => ({}));
    const keys = Array.isArray(set.keys) ? set.keys : [];
    const matched = keys.some(
      (key) => key.kty === "OKP" && key.crv === "Ed25519" && key.x === this._expectedKey,
    );
    if (!matched) throw new KeyMismatchError();
  }

  async _register() {
    const registered = await this._request("POST", "/peers");
    this.peerId = registered.peer_id;
  }

  async _loadICE() {
    const config = await this._request("GET", "/ice-config");
    this._iceServers = config.ice_servers ?? [];
    this.relayAvailable = Boolean(config.relay_available);
  }

  /** One authenticated request, refreshing the token when it has run out. */
  async _request(method, path, body = null) {
    await this._refreshTokenIfNeeded();

    const response = await this._fetch(`${this.serverURL}${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${this._token}`,
        ...(body ? { "Content-Type": "application/json" } : {}),
      },
      ...(body ? { body: JSON.stringify(body) } : {}),
    });

    if (response.status === 401) {
      // The token was refused rather than merely old. One retry with a fresh one
      // covers a clock that drifted; a second failure is a real refusal.
      await this._authenticate();
      return this._request(method, path);
    }
    if (!response.ok) {
      const failed = await response.json().catch(() => ({}));
      throw new Error(`${method} ${path}: ${failed.error ?? response.status}`);
    }
    if (response.status === 204) return null;

    return response.json();
  }

  async _refreshTokenIfNeeded() {
    const margin = 30_000;
    if (this._token && Date.now() < this._tokenExpiresAt - margin) return;
    await this._authenticate();
  }

  async _openChannel() {
    const url = `${this.serverURL.replace(/^http/, "ws")}/signal`;
    const socket = new this._WebSocket(url, [SUBPROTOCOL, TOKEN_PREFIX + this._token]);
    this._socket = socket;

    socket.onmessage = (event) => this._onMessage(event);
    socket.onclose = () => this._onChannelClosed(socket);

    if (socket.readyState !== 1) {
      await new Promise((resolve, reject) => {
        socket.onopen = resolve;
        socket.onerror = () => reject(new Error("the signaling channel would not open"));
      });
    }

    this._reconnectAttempt = 0;
    this.emit("channel", { open: true });
  }

  _onChannelClosed(socket) {
    if (this._socket !== socket) return;
    this._socket = null;
    this.emit("channel", { open: false });

    // A call that is still negotiating cannot finish without the channel. One
    // already connected does not need it — the media path is not the server.
    for (const call of [...this._calls.values()]) {
      if (call.state === CallState.Connecting) {
        this._closeCall(call, CallState.Failed).catch(() => {});
      }
    }

    if (this._closed) return;
    this._reconnect().catch((err) => this.emit("error", err));
  }

  async _reconnect() {
    while (!this._closed && !this._socket) {
      await this._sleep(this._backoffDelay(this._reconnectAttempt));
      this._reconnectAttempt += 1;

      try {
        await this._refreshTokenIfNeeded();
        await this._register();
        await this._openChannel();
      } catch (err) {
        this.emit("error", err);
      }
    }
  }

  /**
   * Doubling, capped, with jitter. The jitter matters more than the backoff: a
   * server restarting has every peer reconnect at once, and identical timers turn
   * one restart into a second outage.
   */
  _backoffDelay(attempt) {
    const base = Math.min(500 * 2 ** attempt, 30_000);
    return Math.round(base * (0.5 + this._random() * 0.5));
  }

  _send(message) {
    if (!this._socket) return false;
    this._socket.send(JSON.stringify(message));
    return true;
  }

  _onMessage(event) {
    let message;
    try {
      message = JSON.parse(event.data);
    } catch {
      return;
    }

    switch (message.type) {
      case "offer":
        this._onOffer(message).catch((err) => this.emit("error", err));
        return;
      case "answer":
        this._onAnswer(message).catch((err) => this.emit("error", err));
        return;
      case "candidate":
        this._onCandidate(message).catch((err) => this.emit("error", err));
        return;
      case "bye":
      case "session":
        this._onEnded(message).catch((err) => this.emit("error", err));
        return;
      case "error":
        this.emit("error", new Error(message.code ?? "signaling error"));
        return;
      default:
        // A type this SDK does not know is not a failure: the server may carry
        // more than this version reads.
    }
  }

  async _onOffer(message) {
    const call = this._callFor(message.session_id, message.from, false);

    await call._connection.setRemoteDescription(message.payload);
    const answer = await call._connection.createAnswer();
    await call._connection.setLocalDescription(answer);

    this._send({ type: "answer", session_id: call.sessionId, payload: answer });
  }

  async _onAnswer(message) {
    const call = this._calls.get(message.session_id);
    if (!call) return;
    await call._connection.setRemoteDescription(message.payload);
  }

  async _onCandidate(message) {
    const call = this._calls.get(message.session_id);
    if (!call) return;
    await call._connection.addIceCandidate(message.payload);
  }

  async _onEnded(message) {
    const call = this._calls.get(message.session_id);
    if (!call) return;
    await this._closeCall(call, CallState.Closed, { tellServer: false, tellPeer: false });
  }

  /** Builds a call, or answers with the one this session already has. */
  _callFor(sessionId, peerId, isCaller) {
    const held = this._calls.get(sessionId);
    if (held) return held;

    const connection = new this._RTCPeerConnection({ iceServers: this._iceServers });
    const call = new Call({ sessionId, peerId, isCaller, connection, client: this });
    this._calls.set(sessionId, call);

    connection.onicecandidate = (event) => {
      if (!event.candidate) return;
      this._send({ type: "candidate", session_id: sessionId, payload: event.candidate });
    };

    connection.ontrack = (event) => {
      call.remoteStream = event.streams?.[0] ?? null;
      call.emit("track", call.remoteStream);
    };

    connection.onconnectionstatechange = () => {
      this._onConnectionState(call).catch((err) => this.emit("error", err));
    };

    if (!isCaller) this.emit("call", call);
    return call;
  }

  async _onConnectionState(call) {
    const state = call._connection.connectionState;

    if (state === "connected") {
      const path = await this._pathOf(call._connection);
      call._moveTo(CallState.Connected, path);
      await this._reportState(call, "connected", path).catch(() => {});
      return;
    }

    if (state === "failed") {
      await this._closeCall(call, CallState.Failed);
    }
  }

  /**
   * Reads the path from the pair that won. A relay candidate on either end means
   * the media is relayed; anything else is direct.
   */
  async _pathOf(connection) {
    if (typeof connection.getStats !== "function") return Path.Unknown;

    const stats = await connection.getStats();
    let pairId = null;
    const candidates = new Map();

    stats.forEach((report) => {
      if (report.type === "transport" && report.selectedCandidatePairId) {
        pairId = report.selectedCandidatePairId;
      }
      if (report.type === "candidate-pair" && report.selected && !pairId) {
        pairId = report.id;
      }
      if (report.type === "local-candidate" || report.type === "remote-candidate") {
        candidates.set(report.id, report);
      }
    });

    if (!pairId) return Path.Unknown;
    const pair = stats.get ? stats.get(pairId) : null;
    if (!pair) return Path.Unknown;

    const local = candidates.get(pair.localCandidateId);
    const remote = candidates.get(pair.remoteCandidateId);
    const relayed = local?.candidateType === "relay" || remote?.candidateType === "relay";

    return relayed ? Path.Relayed : Path.Direct;
  }

  async _reportState(call, state, path) {
    await this._request("POST", `/sessions/${call.sessionId}/state`, {
      state,
      ...(path ? { path } : {}),
    });
  }

  /** Ends a call, telling whoever still needs telling. */
  async _closeCall(call, state, { tellServer = true, tellPeer = true } = {}) {
    if (call._closed) return;
    call._closed = true;
    this._calls.delete(call.sessionId);

    if (tellPeer) this._send({ type: "bye", session_id: call.sessionId });

    try {
      call._connection.close();
    } catch {
      // A connection that is already gone is not a failure to close it.
    }

    // Whatever this side was sending stops with the call, or the camera light
    // stays on after a call the user ended.
    for (const track of call._localStream?.getTracks?.() ?? []) {
      track.stop();
    }

    call._moveTo(state);

    if (tellServer) {
      const reported = state === CallState.Failed ? "failed" : "closed";
      await this._reportState(call, reported, null).catch(() => {});
    }
  }
}
