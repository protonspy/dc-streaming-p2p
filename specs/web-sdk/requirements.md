---
autonomy: auto
ci: wait
---

# Web SDK — requirements

## Purpose

Everything the control plane does exists so that an application can say *connect me
to that peer* and get audio and video. This feature is that sentence: one call that
authenticates, registers, opens a session, negotiates, and hands back the far side's
media.

Whether the media travels directly or through the relay is infrastructure. The
application is told which happened, and does nothing differently either way.

## R1 · Joining

- **R1.1** When an application starts the SDK with a server URL and credentials, the SDK shall authenticate, register as its peer, and open its signaling channel.
- **R1.2** Where the application configured an expected public key, the SDK shall refuse a server that publishes any other key before presenting any credential.
- **R1.3** If authentication fails, then the SDK shall report why and shall not retry with the same credentials.
- **R1.4** The SDK shall keep its registration and its channel open until the application closes it.
- **R1.5** If the signaling channel closes while the application has not closed it, then the SDK shall reopen it, waiting longer between attempts as they keep failing.
- **R1.6** If a token expires while the SDK is running, then the SDK shall obtain another and carry on without the application acting.

## R2 · Calling

- **R2.1** When the application asks to connect to a peer, the SDK shall open a session with that peer and negotiate a peer connection.
- **R2.2** The SDK shall send the local audio and video the application gave it, and shall hand back what the far side sends.
- **R2.3** When a peer connection is established, the SDK shall report whether the path is direct or relayed, and shall report the same to the control plane.
- **R2.4** If a peer connection cannot be established, then the SDK shall report the failure to the application and to the control plane, and shall leave no session open.
- **R2.5** When an offer arrives for a session the SDK is in and the application has not started it, the SDK shall answer it as the called side.
- **R2.6** When either side closes a call, the SDK shall close the peer connection, close the session, and stop the local media it was sending.
- **R2.7** The SDK shall behave identically whether the media travels directly or through the relay, other than what it reports.

## R3 · What the application sees

- **R3.1** The SDK shall report the state of a call as it changes, in terms the application can act on.
- **R3.2** The SDK shall carry no session description or candidate into what the application sees.
- **R3.3** If the SDK receives a message for a session it does not have, then it shall discard it rather than fail.
