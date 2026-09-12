// Only the standard WebLiero headless API is used.
// Keep public:false for an unlisted room, or change to true to list it.
var room = window.WLInit({
  token: window.WLTOKEN,
  roomName: "My WebLiero room",
  maxPlayers: 12,
  public: false
});
window.WLROOM = room;
room.onRoomLink = link => console.log(link);
room.onCaptcha = () => console.error("Token rejected. Get a fresh headless token, set WEBLIERO_TOKEN, and restart the launcher.");
room.onPlayerJoin = player => console.log("Joined:", player.name);
room.onPlayerLeave = player => console.log("Left:", player.name);
