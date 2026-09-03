# Scope runtime execution policies to their exact authorities

Status: accepted

An Account owns the Header mutations and protected authentication applied to every request made with that Account; an Environment owns the child-process environment supplied when a Capture Run starts; and a Traffic Path owns the second-hop Egress Policy and AI-message Transform Policy used for its exact Original Destination or Upstream Route. A Capture freezes the published Environment revision, while each upstream attempt freezes its selected Account revision and credential epoch. We reject global proxy, script, or environment switches because they would let unrelated traffic share implicit mutable authority and make retained evidence ambiguous.
