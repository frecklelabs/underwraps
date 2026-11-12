import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
let fixture, url;
test.beforeEach(async () => {
  test.setTimeout(120000);
  fixture = spawn(
    "go",
    [
      "test",
      "./internal/server",
      "-run",
      "^TestBrowserFixture$",
      "-v",
      "-count=1",
    ],
    {
      cwd: "..",
      env: { ...process.env, UW_BROWSER_FIXTURE: "1" },
      detached: true,
    },
  );
  url = await new Promise((resolve, reject) => {
    let out = "";
    const timer = setTimeout(
      () => reject(Error("fixture startup timed out: " + out)),
      90000,
    );
    fixture.stdout.on("data", (chunk) => {
      out += chunk;
      const m = out.match(/FIXTURE_URL=(https:\/\/\S+)/);
      if (m) {
        clearTimeout(timer);
        resolve(m[1]);
      }
    });
    fixture.stderr.on("data", (c) => (out += c));
    fixture.on("exit", (code) => {
      clearTimeout(timer);
      reject(Error("fixture exited " + code + ": " + out));
    });
  });
});
test.afterEach(() => {
  if (fixture?.pid) {
    try {
      process.kill(-fixture.pid, "SIGTERM");
    } catch {}
  }
});
async function login(page, name) {
  await page.goto(url);
  await page.getByRole("link", { name: "Sign in", exact: true }).click();
  await page.getByRole("link", { name, exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Wish lists", exact: true }),
  ).toBeVisible();
}
async function add(page, name, priority = 0, price = "$12") {
  await page.getByRole("button", { name: "Add item", exact: true }).click();
  const form = page.getByRole("form", { name: "Item details" });
  await form.getByLabel("Name", { exact: true }).fill(name);
  await form.getByLabel("Priority", { exact: true }).fill(String(priority));
  await form
    .getByLabel("Link", { exact: true })
    .fill("https://shop.example.invalid/item");
  await form.getByLabel("Price", { exact: true }).fill(price);
  await form.getByLabel("Note", { exact: true }).fill("Manual fixture fields");
  await form
    .getByLabel("Image", { exact: true })
    .fill("https://images.example.invalid/item.png");
  await form.getByRole("button", { name: "Save item", exact: true }).click();
  await expect(page.getByRole("article", { name })).toBeVisible();
}
function selected(page) {
  return page.getByRole("region", { name: "Selected list", exact: true });
}
async function open(page, name) {
  await page
    .getByRole("button")
    .filter({ has: page.getByText(name, { exact: true }) })
    .first()
    .click();
  await expect(
    selected(page).getByRole("heading", { name, exact: true }),
  ).toBeVisible();
}
test("long unbroken content wraps in both phone caller views", async ({
  browser,
}) => {
  const contexts = await Promise.all(
    [0, 1].map(() =>
      browser.newContext({
        ignoreHTTPSErrors: true,
        viewport: { width: 390, height: 844 },
      }),
    ),
  );
  try {
    const [owner, member] = await Promise.all(contexts.map((c) => c.newPage()));
    await login(owner, "Owner");
    const listName = "L".repeat(200);
    const itemName = "W".repeat(300);
    const price = "9".repeat(100);
    const date = new Date();
    date.setUTCDate(date.getUTCDate() + 2);
    await owner.getByText("Create a list", { exact: true }).click();
    const form = owner.locator("details").filter({
      has: owner.getByText("Create a list", { exact: true }),
    });
    await form.getByLabel("List name", { exact: true }).fill(listName);
    await form
      .getByLabel("Occasion date", { exact: true })
      .fill(date.toISOString().slice(0, 10));
    await form.getByLabel("Fixture family", { exact: true }).check();
    await form
      .getByRole("button", { name: "Create list", exact: true })
      .click();
    await open(owner, listName);
    await add(owner, itemName, 0, price);
    await expect(
      owner.getByRole("article", { name: itemName, exact: true }),
    ).toContainText(price);
    await login(member, "Second");
    for (const page of [owner, member]) {
      await page.reload();
      await expect(page.getByText(listName, { exact: true })).toBeVisible();
      await expect
        .poll(() =>
          page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        )
        .toBe(true);
      await open(page, listName);
      const item = page.getByRole("article", { name: itemName, exact: true });
      await expect(
        item.getByRole("heading", { name: itemName, exact: true }),
      ).toBeVisible();
      await expect(item).toContainText(price);
      await expect
        .poll(() =>
          page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        )
        .toBe(true);
      for (const control of await item.getByRole("button").all()) {
        const box = await control.boundingBox();
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(390);
      }
    }
  } finally {
    await Promise.all(contexts.map((c) => c.close()));
  }
});

test("phone owner and second person preserve secrecy through coordination and archive", async ({
  browser,
}) => {
  const a = await browser.newContext({
      ignoreHTTPSErrors: true,
      viewport: { width: 390, height: 844 },
    }),
    b = await browser.newContext({
      ignoreHTTPSErrors: true,
      viewport: { width: 390, height: 844 },
    });
  const owner = await a.newPage(),
    second = await b.newPage();
  let remoteRequests = [];
  owner.on("request", (r) => {
    if (
      r.url().includes("shop.example.invalid") ||
      r.url().includes("images.example.invalid")
    )
      remoteRequests.push(r.url());
  });
  await login(owner, "Owner");
  const future = new Date();
  future.setUTCDate(future.getUTCDate() + 2);
  const date = future.toISOString().slice(0, 10);
  for (const name of ["Fixture celebration", "Another occasion"]) {
    await owner.getByText("Create a list", { exact: true }).click();
    const form = owner
      .locator("details")
      .filter({ has: owner.getByText("Create a list", { exact: true }) });
    await form.getByLabel("List name", { exact: true }).fill(name);
    await form.getByLabel("Occasion date", { exact: true }).fill(date);
    await form.getByLabel("Fixture family", { exact: true }).check();
    await form
      .getByRole("button", { name: "Create list", exact: true })
      .click();
    await expect(
      owner
        .getByRole("button")
        .filter({ has: owner.getByText(name, { exact: true }) }),
    ).toBeVisible();
    await owner.getByText("Create a list", { exact: true }).click();
  }
  await open(owner, "Fixture celebration");
  await add(owner, "Lower wish");
  await add(owner, "Top wish");
  await owner
    .getByRole("button", {
      name: "Increase priority for Top wish",
      exact: true,
    })
    .click();
  await expect(selected(owner).getByRole("article").first()).toHaveAttribute(
    "aria-label",
    "Top wish",
  );
  await login(second, "Second");
  await open(second, "Fixture celebration");
  await expect(selected(second).getByRole("article").first()).toHaveAttribute(
    "aria-label",
    "Top wish",
  );
  await second
    .getByRole("article", { name: "Top wish", exact: true })
    .getByRole("button", { name: "Mark bought", exact: true })
    .click();
  await expect(
    second.getByText("Bought by Second", { exact: true }),
  ).toBeVisible();
  await second
    .getByRole("button", { name: "Add surprise", exact: true })
    .click();
  const surprise = second.getByRole("form", { name: "Item details" });
  await surprise.getByLabel("Name", { exact: true }).fill("HIDDEN PRESENT");
  await surprise
    .getByRole("button", { name: "Save item", exact: true })
    .click();
  await expect(
    second.getByRole("article", { name: "HIDDEN PRESENT" }),
  ).toBeVisible();
  let ownerBodies = [];
  owner.on("response", async (r) => {
    if (r.url().includes("/api/")) {
      try {
        ownerBodies.push(await r.text());
      } catch {}
    }
  });
  await owner.reload();
  await open(owner, "Fixture celebration");
  await expect(selected(owner).getByRole("article")).toHaveCount(2);
  await expect(selected(owner)).not.toContainText("HIDDEN PRESENT");
  await expect(selected(owner)).not.toContainText("Bought");
  // A claimed and an unclaimed request expose identical owner controls and deleted-item fields.
  for (const name of ["Top wish", "Lower wish"]) {
    await owner
      .getByRole("article", { name, exact: true })
      .getByRole("button", { name: "Delete", exact: true })
      .click();
    await expect(owner.getByRole("article", { name, exact: true })).toHaveCount(
      0,
    );
  }
  // Check the member projection while both requests are still deleted.
  await second.reload();
  await open(second, "Fixture celebration");
  const removed = second.getByRole("article", {
    name: "Top wish",
    exact: true,
  });
  await expect(removed).toContainText("Removed by owner");
  await expect(removed).toContainText("Bought by Second");
  await expect(
    second.getByRole("article", { name: "Lower wish", exact: true }),
  ).toHaveCount(0);
  await expect(selected(second).getByRole("article")).toHaveCount(2);
  await owner
    .getByRole("button", { name: "Deleted items", exact: true })
    .click();
  await expect(selected(owner).getByRole("article")).toHaveCount(2);
  for (const name of ["Top wish", "Lower wish"]) {
    await owner
      .getByRole("article", { name, exact: true })
      .getByRole("button", { name: "Restore", exact: true })
      .click();
    await expect(owner.getByRole("article", { name, exact: true })).toHaveCount(
      0,
    );
  }
  await owner
    .getByRole("button", { name: "Requested items", exact: true })
    .click();
  await expect(selected(owner).getByRole("article")).toHaveCount(2);
  await second.reload();
  await open(second, "Fixture celebration");
  await expect(
    second.getByText("Bought by Second", { exact: true }),
  ).toBeVisible();
  await expect(
    second.getByRole("article", { name: "HIDDEN PRESENT" }),
  ).toBeVisible();
  await expect(selected(second)).not.toContainText("Removed by owner");
  await expect(
    second.getByRole("article", { name: "Lower wish", exact: true }),
  ).toBeVisible();
  await expect(selected(second).getByRole("article")).toHaveCount(3);
  await owner.request.post(url + "/fixture/advance");
  await owner.getByRole("button", { name: "Archive", exact: true }).click();
  await open(owner, "Fixture celebration");
  await expect(selected(owner)).toContainText("Read only");
  await expect(selected(owner).getByRole("article")).toHaveCount(2);
  await expect(
    selected(owner).getByRole("button", { name: "Delete", exact: true }),
  ).toHaveCount(0);
  await second.getByRole("button", { name: "Archive", exact: true }).click();
  await open(second, "Fixture celebration");
  await expect(
    second.getByRole("article", { name: "HIDDEN PRESENT" }),
  ).toBeVisible();
  await expect(
    second.getByText("Bought by Second", { exact: true }),
  ).toBeVisible();
  await expect(
    selected(second).getByRole("button", { name: "Mark bought", exact: true }),
  ).toHaveCount(0);
  await expect(owner.locator("body")).not.toContainText("HIDDEN PRESENT");
  await expect(owner.locator("body")).not.toContainText("Bought by");
  expect(ownerBodies.length).toBeGreaterThan(0);
  for (const body of ownerBodies) {
    expect(body).not.toMatch(/Buyer|Surprise|Creator|HIDDEN PRESENT/);
  }
  expect(remoteRequests).toEqual([]);
  expect(
    await owner.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await a.close();
  await b.close();
});

test.afterEach(async ({ browser }, info) => {
  if (info.status !== info.expectedStatus) {
    let n = 0;
    for (const c of browser.contexts()) {
      for (const p of c.pages()) {
        await p
          .screenshot({ path: info.outputPath(`failure-${n++}.png`) })
          .catch(() => {});
      }
    }
  }
});
