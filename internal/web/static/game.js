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
    { id: "lion", glyph: "L", color: "#e8a14b" },
    { id: "zebra", glyph: "Z", color: "#f2f5f1" },
    { id: "rhino", glyph: "R", color: "#9aa7b3" },
    { id: "giraffe", glyph: "G", color: "#d8a853" },
    { id: "panda", glyph: "P", color: "#f0f3f5" },
    { id: "hippo", glyph: "H", color: "#9d82b8" }
  ];

  let worker = { x: 0, y: 0 };
  let badGuy = { x: 72, y: 78, dx: 1, dy: 1 };
  let animalState = [];
  let badGuyTimer = 0;

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
    el.style.transform = "translate(" + point.x + "px, " + point.y + "px)";
  }

  function drawAnimals() {
    animalState.forEach((animal, index) => {
      if (animal.caught) {
        if (!zooSlots.contains(animal.el)) zooSlots.appendChild(animal.el);
        animal.el.classList.add("caught");
        animal.el.style.transform = "";
        animal.el.style.background = animal.color;
        animal.el.style.order = String(index);
        return;
      }
      if (!board.contains(animal.el)) board.appendChild(animal.el);
      animal.el.classList.remove("caught");
      animal.el.style.background = animal.color;
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
      message.textContent = remaining ? "Animal caught." : "All animals safe.";
    }
  }

  function moveWorker(dx, dy) {
    const boardSize = currentBoardSize();
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
    zooSlots.innerHTML = "";
    animalState.forEach((animal) => animal.el.remove());
    animalState = animals.map((animal) => {
      const point = randomOutsideZoo();
      const el = document.createElement("div");
      el.className = "animal";
      el.textContent = animal.glyph;
      el.setAttribute("aria-label", animal.id);
      el.title = "";
      return { ...animal, ...point, caught: false, el };
    });
    message.textContent = "Use arrow keys or WASD.";
    draw();
    board.focus();
    badGuyTimer = window.setInterval(tickBadGuy, 90);
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

  newGame.addEventListener("click", resetGame);
  createBadGuySprite();
  resetGame();
}());
