// The demonstration: two tabs, one call, and a log of what the SDK reported. It
// exists to prove the path end to end, not to be a product.

import { CallState, PeerClient } from "../sdk/client.js";

const elements = {
  join: document.querySelector("#join"),
  joinButton: document.querySelector("#join-button"),
  joinStatus: document.querySelector("#join-status"),
  clientId: document.querySelector("#client-id"),
  clientSecret: document.querySelector("#client-secret"),
  call: document.querySelector("#call"),
  callButton: document.querySelector("#call-button"),
  hangupButton: document.querySelector("#hangup-button"),
  callStatus: document.querySelector("#call-status"),
  peerId: document.querySelector("#peer-id"),
  local: document.querySelector("#local"),
  remote: document.querySelector("#remote"),
  log: document.querySelector("#log"),
};

let client = null;
let localStream = null;
let activeCall = null;

function log(message) {
  const entry = document.createElement("li");
  entry.textContent = `${new Date().toLocaleTimeString()} — ${message}`;
  elements.log.prepend(entry);
}

/** Attaches a call to the page, whichever side started it. */
function adopt(call) {
  activeCall = call;
  elements.hangupButton.disabled = false;
  elements.callStatus.textContent = `${call.isCaller ? "calling" : "answering"} ${call.peerId}…`;
  log(`${call.isCaller ? "calling" : "answering"} ${call.peerId} — session ${call.sessionId}`);

  call.on("track", (stream) => {
    elements.remote.srcObject = stream;
    log("the other side's media arrived");
  });

  call.on("state", ({ state, path }) => {
    elements.callStatus.textContent = path ? `${state} (${path})` : state;
    log(path ? `call ${state}, ${path}` : `call ${state}`);

    if (state === CallState.Closed || state === CallState.Failed) {
      activeCall = null;
      elements.hangupButton.disabled = true;
      elements.remote.srcObject = null;
    }
  });
}

elements.join.addEventListener("submit", async (event) => {
  event.preventDefault();
  elements.joinButton.disabled = true;

  try {
    localStream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true });
    elements.local.srcObject = localStream;

    client = new PeerClient({
      serverURL: window.location.origin,
      clientID: elements.clientId.value,
      clientSecret: elements.clientSecret.value,
    });

    client.on("call", adopt);
    client.on("channel", ({ open }) => log(open ? "signaling channel open" : "signaling channel closed"));
    client.on("error", (err) => log(`error: ${err.message}`));

    const peerId = await client.start();
    elements.joinStatus.textContent = `Joined as ${peerId}.`;
    elements.callButton.disabled = false;
    log(`joined as ${peerId}${client.relayAvailable ? "" : " — no relay configured"}`);
  } catch (err) {
    elements.joinStatus.textContent = `Could not join: ${err.message}`;
    elements.joinButton.disabled = false;
    log(`could not join: ${err.message}`);
  }
});

elements.call.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!client) return;

  try {
    adopt(await client.call(elements.peerId.value, { localStream }));
  } catch (err) {
    elements.callStatus.textContent = `Could not call: ${err.message}`;
    log(`could not call: ${err.message}`);
  }
});

elements.hangupButton.addEventListener("click", async () => {
  if (!activeCall) return;
  await activeCall.close();
});

window.addEventListener("pagehide", () => {
  // Leaving loudly is a far side that stops waiting sooner.
  client?.close();
});
