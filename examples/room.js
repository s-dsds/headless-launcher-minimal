// Only the standard WebLiero headless API is used.
// Enable "My scripts call WLInit" in the panel. Its settings override these defaults.
var room = window.WLInit({
  token: window.WLTOKEN,
  roomName: "My WebLiero room",
  maxPlayers: 12,
  public: false
});
window.WLROOM = room;
room.onRoomLink = link => console.log(link);
room.onCaptcha = () => console.error("Token rejected. Stop this room and start it from the panel with a fresh token.");
room.onPlayerJoin = player => console.log("Joined:", player.name);
room.onPlayerLeave = player => console.log("Left:", player.name);
