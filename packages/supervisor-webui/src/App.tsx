import { Route, Switch } from "wouter";
import { Live } from "./live/Live";
import { Preview } from "./preview/Preview";

export function App() {
  return (
    <Switch>
      <Route path="/preview" component={Preview} />
      <Route path="/" component={Live} />
      <Route>
        <div className="flex h-dvh items-center justify-center text-muted-foreground">
          <div className="space-y-2 text-center">
            <div className="text-lg font-semibold">ptydemo</div>
            <div className="text-sm">
              <a href="/" className="underline">
                /
              </a>{" "}
              — live sessions &nbsp;·&nbsp;{" "}
              <a href="/preview" className="underline">
                /preview
              </a>{" "}
              — mock data
            </div>
          </div>
        </div>
      </Route>
    </Switch>
  );
}
