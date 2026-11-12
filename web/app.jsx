import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./style.css";
let csrf = "";
async function api(op, args = {}) {
  const r = await fetch("/api/" + op, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    body: JSON.stringify(args),
  });
  const v = await r.json();
  if (!r.ok) throw Error(v.error);
  return v;
}
const empty = {
  Name: "",
  Link: "",
  Price: "",
  Note: "",
  Image: "",
  Quantity: 1,
  Priority: 0,
};
function safeLink(value) {
  try {
    const u = new URL(value);
    return ["https:", "http:"].includes(u.protocol) &&
      !u.username &&
      !u.password
      ? u.href
      : null;
  } catch {
    return null;
  }
}
function App() {
  const [me, setMe] = useState(null),
    [ready, setReady] = useState(false),
    [error, setError] = useState(""),
    [lists, setLists] = useState([]),
    [archive, setArchive] = useState(false),
    [view, setView] = useState(null),
    [deleted, setDeleted] = useState(false),
    [listPage, setListPage] = useState(0),
    [page, setPage] = useState(0),
    [busy, setBusy] = useState(false),
    [editing, setEditing] = useState(null),
    [request, setRequest] = useState(empty),
    [showForm, setShowForm] = useState(false),
    [users, setUsers] = useState(null),
    [groups, setGroups] = useState(null);
  const body = view?.owner || view?.member;
  const owner = !!view?.owner;
  async function run(fn) {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }
  async function loadLists(arch = archive, offset = listPage) {
    setLists(await api("lists", { archived: arch, offset, limit: 20 }));
  }
  async function load(id = body?.List.ID, del = deleted, offset = page) {
    if (!id) return;
    const v = await api(del ? "deleted" : "detail", {
      list: id,
      offset,
      limit: 20,
    });
    setView(del ? { owner: v } : v);
  }
  useEffect(() => {
    api("me")
      .then((v) => {
        csrf = v.CSRF;
        setMe(v.Account);
        return loadLists(false, 0);
      })
      .catch((e) => {
        if (e.message !== "sign in required") setError(e.message);
      })
      .finally(() => setReady(true));
  }, []);
  async function mutate(op, args) {
    await api(op, args);
    await load();
    await loadLists();
  }
  function open(summary) {
    run(async () => {
      setDeleted(false);
      setPage(0);
      setEditing(null);
      setShowForm(false);
      await load(summary.List.ID, false, 0);
    });
  }
  if (!ready)
    return (
      <main>
        <p>Opening your lists…</p>
      </main>
    );
  if (!me)
    return (
      <main>
        <h1>Under Wraps</h1>
        <p>Wish lists for every occasion.</p>
        {error && <p role="alert">{error}</p>}
        <a className="button" href="/auth/login">
          Sign in
        </a>
      </main>
    );
  return (
    <main>
      <header>
        <h1>Under Wraps</h1>
        <span>{me.User.Name}</span>
        <button
          onClick={() =>
            run(async () => {
              await api("logout");
              location.assign("/");
            })
          }
        >
          Sign out
        </button>
      </header>
      {error && <p role="alert">{error}</p>}
      <fieldset disabled={busy}>
        <nav>
          <button
            aria-pressed={!archive}
            onClick={() =>
              run(async () => {
                setArchive(false);
                setListPage(0);
                setView(null);
                await loadLists(false, 0);
              })
            }
          >
            Upcoming
          </button>
          <button
            aria-pressed={archive}
            onClick={() =>
              run(async () => {
                setArchive(true);
                setListPage(0);
                setView(null);
                await loadLists(true, 0);
              })
            }
          >
            Archive
          </button>
        </nav>
        <section aria-label="Lists">
          <h2>{archive ? "Archived lists" : "Wish lists"}</h2>
          {lists.length === 0 && <p>No lists here yet.</p>}
          {lists.map((s) => (
            <button className="list" key={s.List.ID} onClick={() => open(s)}>
              <strong>{s.List.Name}</strong>
              <span>
                {s.List.Occasion} · {s.Count} items
                {s.List.OwnerID === me.User.ID ? " · Yours" : ""}
              </span>
            </button>
          ))}
          <div className="row">
            <button
              disabled={listPage === 0}
              onClick={() =>
                run(async () => {
                  setListPage(listPage - 20);
                  await loadLists(archive, listPage - 20);
                })
              }
            >
              Previous lists
            </button>
            <button
              disabled={lists.length < 20}
              onClick={() =>
                run(async () => {
                  setListPage(listPage + 20);
                  await loadLists(archive, listPage + 20);
                })
              }
            >
              More lists
            </button>
          </div>
        </section>
        {!archive && (
          <details>
            <summary>Create a list</summary>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const f = new FormData(e.currentTarget);
                const form = e.currentTarget;
                run(async () => {
                  await api("create_list", {
                    name: f.get("name"),
                    date: f.get("date"),
                    groups: f.getAll("groups"),
                  });
                  form.reset();
                  await loadLists();
                });
              }}
            >
              <label>
                List name
                <input name="name" required maxLength="200" />
              </label>
              <label>
                Occasion date
                <input name="date" type="date" required />
              </label>
              <fieldset>
                <legend>Share with</legend>
                {me.Groups.map((g) => (
                  <label className="check" key={g.ID}>
                    <input type="checkbox" name="groups" value={g.ID} />
                    {g.Name}
                  </label>
                ))}
              </fieldset>
              <button>Create list</button>
            </form>
          </details>
        )}
        {body && (
          <section aria-label="Selected list">
            <h2>{body.List.Name}</h2>
            <p>
              {body.List.Occasion}
              {body.List.Archived ? " · Read only" : ""} · {body.Count} items
            </p>
            {owner && (
              <div className="row">
                <button
                  aria-pressed={!deleted}
                  onClick={() =>
                    run(async () => {
                      setDeleted(false);
                      setPage(0);
                      await load(body.List.ID, false, 0);
                    })
                  }
                >
                  Requested items
                </button>
                <button
                  aria-pressed={deleted}
                  onClick={() =>
                    run(async () => {
                      setDeleted(true);
                      setPage(0);
                      await load(body.List.ID, true, 0);
                    })
                  }
                >
                  Deleted items
                </button>
              </div>
            )}
            {body.Items.map((i) => (
              <article key={i.ID} aria-label={i.Name}>
                <h3>{i.Name}</h3>
                <p>
                  {i.Price} · Quantity {i.Quantity} · Priority {i.Priority}
                </p>
                {i.Note && <p className="note">{i.Note}</p>}
                {safeLink(i.Link) && (
                  <a
                    target="_blank"
                    rel="noopener noreferrer"
                    href={safeLink(i.Link)}
                  >
                    Product link
                  </a>
                )}
                {safeLink(i.Image) && (
                  <a
                    target="_blank"
                    rel="noopener noreferrer"
                    href={safeLink(i.Image)}
                  >
                    Image link
                  </a>
                )}
                {!owner && (
                  <p>
                    {i.Surprise ? "Surprise · " : ""}
                    {i.RemovedByOwner ? "Removed by owner · " : ""}
                    {i.BuyerID ? `Bought by ${i.BuyerName}` : "Available"}
                  </p>
                )}
                {!body.List.Archived && (
                  <div className="row">
                    {owner ? (
                      deleted ? (
                        <button
                          onClick={() =>
                            run(() =>
                              mutate("restore_request", {
                                list: body.List.ID,
                                item: i.ID,
                              }),
                            )
                          }
                        >
                          Restore
                        </button>
                      ) : (
                        <>
                          <button
                            aria-label={"Increase priority for " + i.Name}
                            disabled={i.Priority >= 1000000}
                            onClick={() =>
                              run(() =>
                                mutate("update_request", {
                                  list: body.List.ID,
                                  item: i.ID,
                                  request: {
                                    ...pick(i),
                                    Priority: i.Priority + 1,
                                  },
                                }),
                              )
                            }
                          >
                            ↑ More wanted
                          </button>
                          <button
                            aria-label={"Decrease priority for " + i.Name}
                            disabled={i.Priority === 0}
                            onClick={() =>
                              run(() =>
                                mutate("update_request", {
                                  list: body.List.ID,
                                  item: i.ID,
                                  request: {
                                    ...pick(i),
                                    Priority: i.Priority - 1,
                                  },
                                }),
                              )
                            }
                          >
                            ↓ Less wanted
                          </button>
                          <button
                            onClick={() => {
                              setEditing(i.ID);
                              setRequest(pick(i));
                              setShowForm(true);
                            }}
                          >
                            Edit
                          </button>
                          <button
                            onClick={() =>
                              run(() =>
                                mutate("delete_request", {
                                  list: body.List.ID,
                                  item: i.ID,
                                }),
                              )
                            }
                          >
                            Delete
                          </button>
                        </>
                      )
                    ) : (
                      <>
                        {!i.BuyerID && (
                          <button
                            onClick={() =>
                              run(() =>
                                mutate("claim", {
                                  list: body.List.ID,
                                  item: i.ID,
                                }),
                              )
                            }
                          >
                            Mark bought
                          </button>
                        )}
                        {i.BuyerID === me.User.ID && (
                          <button
                            onClick={() =>
                              run(() =>
                                mutate("release_claim", {
                                  list: body.List.ID,
                                  item: i.ID,
                                }),
                              )
                            }
                          >
                            Release claim
                          </button>
                        )}
                        {i.Surprise &&
                          !i.RemovedByOwner &&
                          i.CreatorID === me.User.ID && (
                            <button
                              onClick={() =>
                                run(() =>
                                  mutate("delete_surprise", {
                                    list: body.List.ID,
                                    item: i.ID,
                                  }),
                                )
                              }
                            >
                              Delete surprise
                            </button>
                          )}
                      </>
                    )}
                  </div>
                )}
              </article>
            ))}
            <div className="row">
              <button
                disabled={page === 0}
                onClick={() =>
                  run(async () => {
                    setPage(page - 20);
                    await load(body.List.ID, deleted, page - 20);
                  })
                }
              >
                Previous items
              </button>
              <button
                disabled={page + body.Items.length >= body.Count}
                onClick={() =>
                  run(async () => {
                    setPage(page + 20);
                    await load(body.List.ID, deleted, page + 20);
                  })
                }
              >
                More items
              </button>
            </div>
            {!body.List.Archived && !deleted && (
              <>
                <button
                  onClick={() => {
                    setEditing(null);
                    setRequest(empty);
                    setShowForm(true);
                  }}
                >
                  {owner ? "Add item" : "Add surprise"}
                </button>
                {showForm && (
                  <form
                    aria-label="Item details"
                    onSubmit={(e) => {
                      e.preventDefault();
                      run(async () => {
                        await mutate(
                          editing
                            ? "update_request"
                            : owner
                              ? "add_request"
                              : "add_surprise",
                          {
                            list: body.List.ID,
                            item: editing || undefined,
                            request,
                          },
                        );
                        setShowForm(false);
                        setEditing(null);
                        setRequest(empty);
                      });
                    }}
                  >
                    <h3>
                      {editing
                        ? "Edit item"
                        : owner
                          ? "New item"
                          : "New surprise"}
                    </h3>
                    {[
                      "Name",
                      "Link",
                      "Price",
                      "Note",
                      "Image",
                      "Quantity",
                      "Priority",
                    ].map((k) => (
                      <label key={k}>
                        {k}
                        {k === "Note" ? (
                          <textarea
                            value={request[k]}
                            onChange={(e) =>
                              setRequest({ ...request, [k]: e.target.value })
                            }
                          />
                        ) : (
                          <input
                            required={k === "Name" || k === "Quantity"}
                            type={
                              ["Quantity", "Priority"].includes(k)
                                ? "number"
                                : ["Link", "Image"].includes(k)
                                  ? "url"
                                  : "text"
                            }
                            min={k === "Quantity" ? 1 : 0}
                            max={k === "Priority" ? 1000000 : undefined}
                            value={request[k]}
                            onChange={(e) =>
                              setRequest({
                                ...request,
                                [k]: ["Quantity", "Priority"].includes(k)
                                  ? Number(e.target.value)
                                  : e.target.value,
                              })
                            }
                          />
                        )}
                      </label>
                    ))}
                    <button>Save item</button>
                    <button type="button" onClick={() => setShowForm(false)}>
                      Cancel
                    </button>
                  </form>
                )}
              </>
            )}
            {owner && !body.List.Archived && (
              <details>
                <summary>Edit list and sharing</summary>
                <form
                  key={body.List.ID}
                  onSubmit={(e) => {
                    e.preventDefault();
                    const f = new FormData(e.currentTarget);
                    run(async () => {
                      await api("update_list", {
                        list: body.List.ID,
                        name: f.get("name"),
                        date: f.get("date"),
                        groups: f.getAll("groups"),
                      });
                      await load();
                      await loadLists();
                    });
                  }}
                >
                  <label>
                    List name
                    <input name="name" defaultValue={body.List.Name} required />
                  </label>
                  <label>
                    Occasion date
                    <input
                      name="date"
                      type="date"
                      defaultValue={body.List.Occasion}
                      required
                    />
                  </label>
                  <fieldset>
                    <legend>Replace sharing groups</legend>
                    {me.Groups.map((g) => (
                      <label className="check" key={g.ID}>
                        <input type="checkbox" name="groups" value={g.ID} />
                        {g.Name}
                      </label>
                    ))}
                  </fieldset>
                  <button>Save list</button>
                </form>
              </details>
            )}
          </section>
        )}
        {me.Admin && (
          <details>
            <summary>Administration</summary>
            <p>
              Manage accounts and groups. List access follows the same sharing
              rules for everyone.
            </p>
            <button
              onClick={() =>
                run(async () => {
                  setUsers(await api("users"));
                  setGroups(await api("groups"));
                })
              }
            >
              Load accounts and groups
            </button>
            {users?.map((u) => (
              <div className="row" key={u.ID}>
                <span>
                  {u.Name}
                  {u.Disabled ? " · Disabled" : ""}
                </span>
                <button
                  onClick={() =>
                    run(async () => {
                      await api("set_user", {
                        user: u.ID,
                        name: u.Name,
                        disabled: !u.Disabled,
                      });
                      setUsers(await api("users"));
                    })
                  }
                >
                  {u.Disabled ? "Enable" : "Disable"} {u.Name}
                </button>
              </div>
            ))}
            {groups?.map((g) => (
              <div className="row" key={g.ID}>
                <span>{g.Name}</span>
                <button
                  onClick={() =>
                    run(async () => {
                      await api("set_group", {
                        group: g.ID,
                        name: g.Name,
                        disabled: !g.Disabled,
                      });
                      setGroups(await api("groups"));
                    })
                  }
                >
                  {g.Disabled ? "Enable" : "Disable"} {g.Name}
                </button>
              </div>
            ))}
          </details>
        )}
      </fieldset>
    </main>
  );
}
function pick(i) {
  return Object.fromEntries(Object.keys(empty).map((k) => [k, i[k]]));
}
createRoot(document.getElementById("root")).render(<App />);
