---
autonomy: auto
ci: wait
---

# Web SDK — tasks

## 1 · Getting on

- [x] 1.1 (Unit) Authenticate against the control plane, pinning the expected public key before sending a credential — R1.1, R1.2, R1.3
- [x] 1.2 (Unit) Register, fetch the ICE configuration, and open the signaling channel with the token in the subprotocol — R1.1, R1.4
  _Depends 1.1_
- [x] 1.3 (Unit) Reopen a channel that closed, waiting longer each time, with jitter, until the application closes the client — R1.5
  _Depends 1.2_
- [x] 1.4 (Unit) Obtain another token when the one held expires, without the application acting — R1.6
  _Depends 1.2_

- [x] 1.5 (Unit) Take a token from the application instead of a secret, and stop reopening the channel when a credential is refused outright — R1.7, R1.8
  _Depends 1.3_
  _Reason The security review found a browser holding a client secret with nothing saying that makes it public, and a refused credential retried forever_

## 2 · Calling

- [x] 2.1 (TDD) Open a session and negotiate as the calling side: offer, candidates, answer — R2.1, R2.2
  _Depends 1.2_
- [x] 2.2 (TDD) Answer as the called side when an offer arrives for a session the application did not start — R2.5
  _Depends 2.1_
- [x] 2.3 (Unit) Report the path once the connection settles, to the application and to the control plane — R2.3, R2.7
  _Depends 2.1_
- [x] 2.4 (Unit) Report a failure to both, leaving no session open — R2.4
  _Depends 2.1_
- [x] 2.5 (Unit) Close a call from either side: the peer connection, the session, and the local media — R2.6
  _Depends 2.1_
- [x] 2.6 (Unit) Report state changes in terms the application acts on, carrying no session description or candidate outward, and discard a message for a session that is not held — R3.1, R3.2, R3.3
  _Depends 2.1_

## 3 · Proving it

- [x] 3.1 (Unit) Serve the SDK and a demonstration page that captures audio and video and calls another peer — R2.1, R2.2
  _Depends 2.5_
- [x] 3.2 (Unit) Run the SDK tests in CI beside the Go suite — R3.1
  _Depends 2.6_
