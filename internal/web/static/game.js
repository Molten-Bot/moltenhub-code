(function () {
  const board = document.getElementById("game-board");
  const zoo = document.getElementById("zoo");
  const zooSlots = document.getElementById("zoo-slots");
  const workerEl = document.getElementById("worker");
  const badGuyEl = document.getElementById("bad-guy");
  const caughtCount = document.getElementById("caught-count");
  const totalCount = document.getElementById("total-count");
  const message = document.getElementById("game-message");
  const newGame = document.getElementById("new-game");

  const actorSize = 38;
  const speed = 22;
  const animals = [
    { id: "lion", glyph: "L" },
    { id: "zebra", glyph: "Z" },
    { id: "rhino", glyph: "R" },
    { id: "giraffe", glyph: "G" },
    { id: "panda", glyph: "P" },
    { id: "hippo", glyph: "H" }
  ];

  let worker = { x: 0, y: 0 };
  let badGuy = { x: 72, y: 78, dx: 1, dy: 1 };
  let animalState = [];
  let badGuyTimer = 0;
  let moveCount = 0;
  let caughtThisRound = 0;

  function trackGameEvent(name, params) {
    const eventName = String(name || "").trim();
    if (!eventName || typeof window.gtag !== "function") return;
    const payload = { send_to: "G-BY33RFG2WB" };
    for (const [key, value] of Object.entries(params || {})) {
      if (typeof value === "string" && value.trim()) {
        payload[key] = value.trim();
        continue;
      }
      if (typeof value === "number" && Number.isFinite(value)) {
        payload[key] = value;
        continue;
      }
      if (typeof value === "boolean") {
        payload[key] = value;
      }
    }
    try {
      window.gtag("event", eventName, payload);
    } catch (_err) {
      // Analytics must not block game input.
    }
  }

  function currentBoardSize() {
    return {
      width: board.clientWidth,
      height: board.clientHeight
    };
  }

  function currentZooRect() {
    const boardBox = board.getBoundingClientRect();
    const zooBox = zoo.getBoundingClientRect();
    return {
      x: zooBox.left - boardBox.left,
      y: zooBox.top - boardBox.top,
      width: zooBox.width,
      height: zooBox.height
    };
  }

  function clamp(value, min, max) {
    return Math.min(Math.max(value, min), max);
  }

  function intersects(a, b, padding) {
    const gap = padding || 0;
    return a.x < b.x + b.width + gap &&
      a.x + actorSize > b.x - gap &&
      a.y < b.y + b.height + gap &&
      a.y + actorSize > b.y - gap;
  }

  function randomOutsideZoo() {
    const boardSize = currentBoardSize();
    const zooRect = currentZooRect();
    for (let i = 0; i < 80; i++) {
      const point = {
        x: Math.floor(Math.random() * (boardSize.width - actorSize)),
        y: Math.floor(Math.random() * (boardSize.height - actorSize))
      };
      if (!intersects(point, zooRect, 28)) return point;
    }
    return { x: 48, y: 420 };
  }

  function createBadGuySprite() {
    const pixels = [
      "0011100",
      "0111110",
      "1101011",
      "1111111",
      "1011101",
      "0011100",
      "0110110",
      "1100011"
    ];
    badGuyEl.innerHTML = "";
    pixels.join("").split("").forEach((pixel) => {
      const cell = document.createElement("span");
      if (pixel === "1") cell.className = "on";
      badGuyEl.appendChild(cell);
    });
  }

  function drawActor(el, point) {
    el.style.setProperty("--zoo-actor-x", point.x + "px");
    el.style.setProperty("--zoo-actor-y", point.y + "px");
  }

  function drawAnimals() {
    animalState.forEach((animal) => {
      if (animal.caught) {
        if (!zooSlots.contains(animal.el)) zooSlots.appendChild(animal.el);
        animal.el.classList.add("caught");
        animal.el.style.removeProperty("--zoo-actor-x");
        animal.el.style.removeProperty("--zoo-actor-y");
        return;
      }
      if (!board.contains(animal.el)) board.appendChild(animal.el);
      animal.el.classList.remove("caught");
      drawActor(animal.el, animal);
    });
  }

  function draw() {
    drawActor(workerEl, worker);
    drawActor(badGuyEl, badGuy);
    drawAnimals();
    caughtCount.textContent = String(animalState.filter((animal) => animal.caught).length);
    totalCount.textContent = String(animalState.length);
  }

  function catchAnimals() {
    let changed = false;
    animalState.forEach((animal) => {
      if (animal.caught) return;
      if (!intersects(worker, { x: animal.x, y: animal.y, width: actorSize, height: actorSize }, 2)) return;
      animal.caught = true;
      changed = true;
    });
    if (changed) {
      const remaining = animalState.filter((animal) => !animal.caught).length;
      caughtThisRound = animalState.length - remaining;
      message.textContent = remaining ? "Animal caught." : "All animals safe.";
      trackGameEvent(remaining ? "game_animal_caught" : "game_completed", {
        caught_count: caughtThisRound,
        remaining_count: remaining,
        move_count: moveCount
      });
    }
  }

  function moveWorker(dx, dy) {
    const boardSize = currentBoardSize();
    moveCount += 1;
    worker.x = clamp(worker.x + dx, 0, boardSize.width - actorSize);
    worker.y = clamp(worker.y + dy, 0, boardSize.height - actorSize);
    catchAnimals();
    draw();
  }

  function tickBadGuy() {
    const boardSize = currentBoardSize();
    badGuy.x += badGuy.dx * 5;
    badGuy.y += badGuy.dy * 4;
    if (badGuy.x < 0 || badGuy.x > boardSize.width - actorSize) badGuy.dx *= -1;
    if (badGuy.y < 0 || badGuy.y > boardSize.height - actorSize) badGuy.dy *= -1;
    badGuy.x = clamp(badGuy.x, 0, boardSize.width - actorSize);
    badGuy.y = clamp(badGuy.y, 0, boardSize.height - actorSize);
    drawActor(badGuyEl, badGuy);
  }

  function resetGame() {
    window.clearInterval(badGuyTimer);
    const zooRect = currentZooRect();
    worker = {
      x: zooRect.x + Math.round((zooRect.width - actorSize) / 2),
      y: zooRect.y + Math.round((zooRect.height - actorSize) / 2)
    };
    badGuy = { x: 72, y: 78, dx: 1, dy: 1 };
    moveCount = 0;
    caughtThisRound = 0;
    zooSlots.innerHTML = "";
    animalState.forEach((animal) => animal.el.remove());
    animalState = animals.map((animal) => {
      const point = randomOutsideZoo();
      const el = document.createElement("div");
      el.className = `animal animal-${animal.id}`;
      el.textContent = animal.glyph;
      el.setAttribute("aria-label", animal.id);
      el.title = "";
      return { ...animal, ...point, caught: false, el };
    });
    message.textContent = "Use arrow keys or WASD.";
    draw();
    board.focus();
    badGuyTimer = window.setInterval(tickBadGuy, 90);
    trackGameEvent("game_started", { animal_count: animalState.length });
  }

  document.addEventListener("keydown", (event) => {
    const key = event.key.toLowerCase();
    const moves = {
      arrowup: [0, -speed],
      w: [0, -speed],
      arrowdown: [0, speed],
      s: [0, speed],
      arrowleft: [-speed, 0],
      a: [-speed, 0],
      arrowright: [speed, 0],
      d: [speed, 0]
    };
    if (!moves[key]) return;
    event.preventDefault();
    moveWorker(moves[key][0], moves[key][1]);
  });

  newGame.addEventListener("click", () => {
    trackGameEvent("game_restarted", {
      caught_count: caughtThisRound,
      move_count: moveCount
    });
    resetGame();
  });
  createBadGuySprite();
  resetGame();
}());
