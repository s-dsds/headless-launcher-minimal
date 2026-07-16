package names

import (
	"math/rand"
	"regexp"
	"strings"
)

var nonWordRe = regexp.MustCompile(`[\W_]+`)

func GetRandomName() string {
	adj := adjectives[rand.Intn(len(adjectives))]
	noun := nouns[rand.Intn(len(nouns))]
	return formatName(adj + "_" + noun)
}

func formatName(name string) string {
	return nonWordRe.ReplaceAllString(strings.TrimSpace(name), "_")
}

var adjectives = []string{
	"adorable", "adventurous", "aggressive", "agreeable", "alert",
	"alive", "amused", "angry", "annoyed", "anxious",
	"arrogant", "ashamed", "attractive", "average", "awful",
	"bad", "beautiful", "better", "bewildered", "black",
	"bloody", "blue", "blushing", "bored", "brainy",
	"brave", "breakable", "bright", "busy", "calm",
	"careful", "cautious", "charming", "cheerful", "clean",
	"clear", "clever", "cloudy", "clumsy", "colorful",
	"combative", "comfortable", "concerned", "condemned", "confused",
	"cooperative", "courageous", "crazy", "creepy", "crowded",
	"cruel", "curious", "cute", "dangerous", "dark",
	"dead", "defeated", "defiant", "delightful", "depressed",
	"determined", "different", "difficult", "disgusted", "distinct",
	"disturbed", "dizzy", "doubtful", "drab", "dull",
	"eager", "easy", "elated", "elegant", "embarrassed",
	"enchanting", "encouraging", "energetic", "enthusiastic", "envious",
	"evil", "excited", "expensive", "exuberant", "fair",
	"faithful", "famous", "fancy", "fantastic", "fierce",
	"filthy", "fine", "foolish", "fragile", "frail",
	"frantic", "friendly", "frightened", "funny", "gentle",
	"gifted", "glamorous", "gleaming", "glorious", "good",
	"gorgeous", "graceful", "grieving", "grotesque", "grumpy",
	"handsome", "happy", "healthy", "helpful", "helpless",
	"hilarious", "homeless", "homely", "horrible", "hungry",
	"hurt", "ill", "important", "impossible", "inexpensive",
	"innocent", "inquisitive", "itchy", "jealous", "jittery",
	"jolly", "joyous", "kind", "lazy", "light",
	"lively", "lonely", "long", "lovely", "lucky",
	"magnificent", "misty", "modern", "motionless", "muddy",
	"mushy", "mysterious", "nasty", "naughty", "nervous",
	"nice", "nutty", "obedient", "obnoxious", "odd",
	"open", "outrageous", "outstanding", "panicky", "perfect",
	"plain", "pleasant", "poised", "poor", "powerful",
	"precious", "prickly", "proud", "puzzled", "quaint",
	"real", "relieved", "repulsive", "rich", "scary",
	"selfish", "shiny", "shy", "silly", "sleepy",
	"smiling", "smoggy", "sore", "sparkling", "splendid",
	"spotless", "stormy", "strange", "stupid", "successful",
	"super", "talented", "tame", "tender", "tense",
	"terrible", "thankful", "thoughtful", "thoughtless", "tired",
	"tough", "troubled", "ugliest", "ugly", "uninterested",
	"unsightly", "unusual", "upset", "uptight", "vast",
	"victorious", "vivacious", "wandering", "weary", "wicked",
	"wild", "witty", "worried", "wrong", "zany",
	"zealous",
}

var nouns = []string{
	"aardvark", "alligator", "alpaca", "ant", "anteater",
	"antelope", "ape", "armadillo", "baboon", "badger",
	"bat", "bear", "beaver", "bee", "bison",
	"boar", "buffalo", "butterfly", "camel", "capybara",
	"caribou", "cat", "caterpillar", "cattle", "chamois",
	"cheetah", "chicken", "chimpanzee", "chinchilla", "clam",
	"cobra", "cockroach", "cod", "coyote", "crab",
	"crane", "crocodile", "crow", "deer", "dinosaur",
	"dog", "dolphin", "donkey", "dove", "dragonfly",
	"duck", "eagle", "echidna", "eel", "elephant",
	"elk", "emu", "falcon", "ferret", "finch",
	"fish", "flamingo", "fly", "fox", "frog",
	"gazelle", "gerbil", "giraffe", "gnat", "gnu",
	"goat", "goose", "goldfish", "gorilla", "grasshopper",
	"grouse", "guanaco", "hamster", "hare", "hawk",
	"hedgehog", "heron", "hippopotamus", "hornet", "horse",
	"hummingbird", "hyena", "ibex", "iguana", "jackal",
	"jaguar", "jay", "jellyfish", "kangaroo", "koala",
	"komodo", "lark", "lemur", "leopard", "lion",
	"llama", "lobster", "locust", "loris", "louse",
	"lyrebird", "magpie", "mallard", "mammoth", "manatee",
	"mandrill", "mink", "mole", "mongoose", "monkey",
	"moose", "mosquito", "mouse", "mule", "narwhal",
	"newt", "nightingale", "octopus", "okapi", "opossum",
	"oryx", "ostrich", "otter", "owl", "ox",
	"oyster", "panther", "parrot", "partridge", "peafowl",
	"pelican", "penguin", "pheasant", "pig", "pigeon",
	"pony", "porcupine", "porpoise", "quail", "rabbit",
	"raccoon", "ram", "rat", "raven", "reindeer",
	"rhinoceros", "salamander", "salmon", "sandpiper", "sardine",
	"scorpion", "seahorse", "seal", "shark", "sheep",
	"skunk", "snail", "snake", "sparrow", "spider",
	"squid", "squirrel", "starling", "stingray", "stork",
	"swallow", "swan", "tapir", "termite", "tiger",
	"toad", "trout", "turkey", "turtle", "viper",
	"vulture", "wallaby", "walrus", "wasp", "weasel",
	"whale", "wolf", "wolverine", "wombat", "woodpecker",
	"worm", "wren", "yak", "zebra",
}
